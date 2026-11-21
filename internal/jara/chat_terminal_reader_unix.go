//go:build linux || darwin

package jara

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// startChatTerminalReader reads the terminal in the background until the
// returned stop function is called. The reader waits in poll(2) beside a wake
// pipe instead of blocking in read(2), so stop can end it. Stop returns only
// after the reader has exited: a blocked read left behind would otherwise
// swallow the next line typed into the terminal, such as the answer to a
// prompt from the Jara that /remote reopens here.
func startChatTerminalReader(input *os.File, chunks chan<- []byte, errs chan<- error) (stop func(), err error) {
	var wake [2]int
	if err = unix.Pipe2(wake[:], unix.O_CLOEXEC); err != nil {
		return nil, err
	}
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		readChatTerminal(int(input.Fd()), wake[0], chunks, errs, done)
	}()
	return func() {
		close(done)
		_, _ = unix.Write(wake[1], []byte{0})
		<-exited
		_ = unix.Close(wake[0])
		_ = unix.Close(wake[1])
	}, nil
}

func readChatTerminal(input, wake int, chunks chan<- []byte, errs chan<- error, done <-chan struct{}) {
	report := func(err error) {
		select {
		case errs <- err:
		case <-done:
		}
	}
	buffer := make([]byte, 4096)
	fds := []unix.PollFd{{Fd: int32(input), Events: unix.POLLIN}, {Fd: int32(wake), Events: unix.POLLIN}}
	for {
		fds[0].Revents, fds[1].Revents = 0, 0
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			report(err)
			return
		}
		if fds[1].Revents != 0 {
			return
		}
		if fds[0].Revents == 0 {
			continue
		}
		n, err := unix.Read(input, buffer)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if n > 0 {
			select {
			case chunks <- append([]byte(nil), buffer[:n]...):
			case <-done:
				return
			}
		}
		if err != nil {
			report(&os.PathError{Op: "read", Path: "/dev/stdin", Err: err})
			return
		}
		if n == 0 {
			report(io.EOF)
			return
		}
	}
}
