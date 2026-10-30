//go:build linux || darwin

package jara

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// A real local PTY preserves OpenSSH authentication, raw mode, resize and signals.
// Only image-paste keystrokes are replaced; terminal output passes through intact.
func clipboardTerminal(r Remote, cmd *exec.Cmd) error {
	fd := int(os.Stdin.Fd())
	if os.Getenv("JARA_IMAGE_PASTE") == "0" || !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return interactive(cmd)
	}
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return err
	}
	master, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return err
	}
	defer master.Close()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	reaped := false
	defer func() {
		if !reaped {
			cmd.Process.Kill()
			<-waited
		}
	}()
	state, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	defer signal.Stop(signals)
	output := make(chan struct{})
	go func() { _, _ = io.Copy(os.Stdout, master); close(output) }()
	defer func() { master.Close(); <-output; _ = term.Restore(fd, state) }()
	write := func(data []byte) error { _, err := master.Write(data); return err }
	var pending bytes.Buffer
	flush := func() error {
		err := write(pending.Bytes())
		pending.Reset()
		return err
	}
	keys := clipboardKeys{write: func(data []byte) error { _, err := pending.Write(data); return err }, image: func() error {
		if err := flush(); err != nil {
			return err
		}
		path, err := uploadClipboardImage(r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\r\njara: %s\r\n", err)
			return nil
		}
		return write([]byte(imagePaste(path)))
	}}
	lastInput := time.Now()
	var buffer [8192]byte
	for {
		select {
		case err := <-waited:
			reaped = true
			// Drain terminal output before restoring the local terminal.
			select {
			case <-output:
			case <-time.After(200 * time.Millisecond):
			}
			return err
		case s := <-signals:
			if s == syscall.SIGWINCH {
				_ = pty.InheritSize(os.Stdin, master)
			} else {
				_ = cmd.Process.Signal(s)
			}
		default:
		}
		if len(keys.sequence) > 0 && time.Since(lastInput) > 80*time.Millisecond {
			if err = keys.flush(); err != nil {
				return err
			}
			if err = flush(); err != nil {
				return err
			}
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err = unix.Poll(fds, 25)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			n, err := unix.Read(fd, buffer[:])
			if err != nil {
				return err
			}
			if n == 0 {
				return io.EOF
			}
			lastInput = time.Now()
			if err = keys.feed(buffer[:n]); err != nil {
				return err
			}
			if err = flush(); err != nil {
				return err
			}
		} else if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return io.EOF
		}
	}
}
