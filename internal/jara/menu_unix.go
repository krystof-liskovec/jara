//go:build unix

package jara

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func terminalMenu(title string, items []menuItem) (int, bool, error) {
	return terminalMenuAt(title, items, 0)
}
func terminalMenuAt(title string, items []menuItem, initial int) (int, bool, error) {
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

	selected, lastWidth, lastHeight := max(0, min(initial, len(items)-1)), 0, 0
	filter, searching := "", false
	redraw := true
	escape := ""
	var escapeAt time.Time
	for {
		select {
		case s := <-signals:
			if s == os.Interrupt {
				return 0, true, ErrUIQuit
			}
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
		matches := menuMatches(items, filter)
		if selected >= len(matches) {
			selected = max(0, len(matches)-1)
		}
		visible := make([]menuItem, len(matches))
		for i, index := range matches {
			visible[i] = items[index]
		}
		if redraw {
			lines := menuLinesFiltered(title, visible, selected, width, height, filter, len(items))
			for len(lines) < height {
				lines = append(lines, "")
			}
			fmt.Print("\x1b[H\x1b[2J" + strings.Join(styleMenuLines(lines, width), "\r\n"))
			lastWidth, lastHeight = width, height
			redraw = false
		}
		if escape != "" && time.Since(escapeAt) > 300*time.Millisecond {
			wasEscape := escape == "\x1b"
			escape = ""
			if wasEscape {
				if filter != "" {
					filter = ""
					selected = 0
					searching = false
					redraw = true
				} else if searching {
					searching = false
					redraw = true
				} else {
					return 0, true, io.EOF
				}
			}
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
		if ch == 3 || ch == 4 {
			return 0, true, ErrUIQuit
		}
		navigation := 0
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
				navigation = -1
			case "\x1b[B", "\x1bOB":
				navigation = 1
			case "\x1b[C", "\x1bOC":
				navigation = 4
			case "\x1b[D", "\x1bOD":
				navigation = -4
			case "\x1b[5~":
				navigation = -2
			case "\x1b[6~":
				navigation = 2
			case "\x1b[H", "\x1b[1~", "\x1bOH":
				navigation = -3
			case "\x1b[F", "\x1b[4~", "\x1bOF":
				navigation = 3
			default:
				escape = ""
				continue
			}
			escape = ""
		}
		if navigation == -4 || (ch == 8 || ch == 127) && (!searching || filter == "") {
			return 0, true, io.EOF
		}
		if height < 7 || width < 16 {
			if ch == 'q' || navigation == 0 && ch == 27 {
				return 0, true, io.EOF
			}
			continue
		}
		if navigation == 4 {
			if len(matches) > 0 {
				return matches[selected], true, nil
			}
			continue
		}
		if navigation != 0 {
			switch navigation {
			case -3:
				selected = 0
			case 3:
				if len(matches) > 0 {
					selected = len(matches) - 1
				}
			case -1:
				selected = max(0, selected-1)
			case 1:
				selected = min(max(0, len(matches)-1), selected+1)
			case -2:
				selected = max(0, selected-menuPageSize(width, height))
			case 2:
				selected = min(max(0, len(matches)-1), selected+menuPageSize(width, height))
			}
			redraw = true
			continue
		}
		if searching {
			switch ch {
			case '\r', '\n':
				if len(matches) > 0 {
					return matches[selected], true, nil
				}
			case 8, 127:
				if filter != "" {
					_, size := utf8.DecodeLastRuneInString(filter)
					filter = filter[:len(filter)-size]
					selected = 0
					redraw = true
				}
			case 21:
				filter, selected, searching, redraw = "", 0, false, true
			default:
				if ch >= 32 {
					filter += string(ch)
					selected = 0
					redraw = true
				}
			}
			continue
		}
		if ch == 'q' {
			return 0, true, io.EOF
		}
		if ch == '/' {
			searching, redraw = true, true
			continue
		}
		switch {
		case ch >= '1' && ch <= '9':
			pageSize := menuPageSize(width, height)
			index := selected/pageSize*pageSize + int(ch-'1')
			if index < min(len(matches), (selected/pageSize+1)*pageSize) {
				return matches[index], true, nil
			}
		case ch == '\r' || ch == '\n':
			if len(matches) > 0 {
				return matches[selected], true, nil
			}
		case ch == 'j':
			selected = min(max(0, len(matches)-1), selected+1)
		case ch == 'k':
			selected = max(0, selected-1)
		case ch == 'n':
			selected = min(max(0, len(matches)-1), selected+menuPageSize(width, height))
		case ch == 'p':
			selected = max(0, selected-menuPageSize(width, height))
		default:
			continue
		}
		redraw = true
	}
}
