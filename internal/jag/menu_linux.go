//go:build linux

package jag

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func terminalMenu(title string, items []menuItem) (int, bool, error) {
	input, output := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if os.Getenv("TERM") == "dumb" || !term.IsTerminal(input) || !term.IsTerminal(output) {
		return 0, false, nil
	}
	state, err := term.MakeRaw(input)
	if err != nil {
		return 0, true, err
	}
	defer term.Restore(input, state)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	fmt.Print("\x1b[?1049h\x1b[?25l")
	defer fmt.Print("\x1b[0m\x1b[?25h\x1b[?1049l")
	selected, lastWidth, lastHeight := 0, 0, 0
	redraw := true
	escape := ""
	var escapeAt time.Time
	for {
		select {
		case s := <-signals:
			return 0, true, fmt.Errorf("menu interrupted: %s", s)
		default:
		}
		width, height, e := term.GetSize(output)
		if e != nil {
			return 0, true, e
		}
		if width <= 0 {
			width = 80
		}
		if height <= 0 {
			height = 24
		}
		if width != lastWidth || height != lastHeight {
			redraw = true
		}
		if redraw {
			fmt.Print("\x1b[H\x1b[2J" + strings.Join(menuLines(title, items, selected, width, height), "\r\n"))
			lastWidth, lastHeight = width, height
			redraw = false
		}
		if escape != "" && time.Since(escapeAt) > 300*time.Millisecond {
			escape = ""
		}
		fds := []unix.PollFd{{Fd: int32(input), Events: unix.POLLIN}}
		_, e = unix.Poll(fds, 100)
		if errors.Is(e, syscall.EINTR) {
			continue
		}
		if e != nil {
			return 0, true, e
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return 0, true, io.EOF
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		var key [1]byte
		n, e := unix.Read(input, key[:])
		if e != nil {
			return 0, true, e
		}
		if n == 0 {
			return 0, true, io.EOF
		}
		ch := key[0]
		if ch == 27 {
			escape = "\x1b"
			escapeAt = time.Now()
			continue
		}
		if escape != "" {
			escape += string(ch)
			if escape == "\x1b[" || escape == "\x1bO" {
				continue
			}
			switch escape {
			case "\x1b[A", "\x1bOA":
				ch = 'k'
			case "\x1b[B", "\x1bOB":
				ch = 'j'
			case "\x1b[5":
				continue
			case "\x1b[6":
				continue
			case "\x1b[5~":
				ch = 'p'
			case "\x1b[6~":
				ch = 'n'
			default:
				escape = ""
				continue
			}
			escape = ""
		}
		if ch == 'q' || ch == 3 || ch == 4 {
			return 0, true, io.EOF
		}
		if height < 7 || width < 16 {
			continue
		}
		size := menuPageSize(width, height)
		switch {
		case ch >= '1' && ch <= '9':
			index := selected/size*size + int(ch-'1')
			if index < min(len(items), (selected/size+1)*size) {
				return index, true, nil
			}
		case ch == '\r' || ch == '\n':
			return selected, true, nil
		case ch == 'j':
			selected = min(len(items)-1, selected+1)
		case ch == 'k':
			selected = max(0, selected-1)
		case ch == 'n':
			selected = min(len(items)-1, selected+size)
		case ch == 'p':
			selected = max(0, selected-size)
		}
		redraw = true
	}
}
