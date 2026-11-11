//go:build linux || darwin

package jara

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func runCodexTerminal(args []string) error {
	if len(args) == 0 || !terminalBinaryAllowed(args[0]) {
		return fmt.Errorf("invalid agent terminal command")
	}
	background, err := terminalBackground()
	if err != nil {
		return err
	}
	args = codexWithoutSharedDaemon(args)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return fmt.Errorf("read terminal size: %w", err)
	}
	master, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return fmt.Errorf("start Codex terminal: %w", err)
	}
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = master.Close()
		return err
	}
	defer func() { _ = term.Restore(fd, state); _ = master.Close() }()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	reaped := false
	defer func() {
		if !reaped {
			_ = cmd.Process.Kill()
			<-waited
		}
	}()
	var writeMu sync.Mutex
	writeMaster := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		for len(data) > 0 {
			n, e := master.Write(data)
			if e != nil {
				return e
			}
			data = data[n:]
		}
		return nil
	}
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		filter := codexColorQueries{answer: func(slot byte) error {
			color := background
			if slot == 10 {
				color = background.foreground()
			}
			return writeMaster(color.oscResponse(slot))
		}}
		buffer := make([]byte, 8192)
		for {
			n, readErr := master.Read(buffer)
			if n > 0 {
				visible, e := filter.feed(buffer[:n])
				if e != nil {
					return
				}
				if _, e = os.Stdout.Write(visible); e != nil {
					return
				}
			}
			if readErr != nil {
				_, _ = os.Stdout.Write(filter.finish())
				return
			}
		}
	}()
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	defer signal.Stop(signals)
	buffer := make([]byte, 8192)
	for {
		select {
		case err = <-waited:
			reaped = true
			<-outputDone
			return err
		case sig := <-signals:
			if sig == syscall.SIGWINCH {
				_ = pty.InheritSize(os.Stdin, master)
			} else {
				_ = cmd.Process.Signal(sig)
			}
		default:
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err = unix.Poll(fds, 25); errors.Is(err, syscall.EINTR) {
			continue
		} else if err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			n, readErr := unix.Read(fd, buffer)
			if n > 0 {
				if err = writeMaster(buffer[:n]); err != nil {
					return err
				}
			}
			if readErr != nil && !errors.Is(readErr, syscall.EINTR) {
				return readErr
			}
		} else if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return io.EOF
		}
	}
}
