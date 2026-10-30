//go:build linux || darwin

package jara

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func detectTerminalBackground() (terminalRGB, error) {
	if color, configured, err := configuredTerminalBackground(); err != nil {
		return terminalRGB{}, err
	} else if configured {
		return color, nil
	}
	if color, ok := probeTerminalBackground(); ok {
		return color, nil
	}
	return fallbackTerminalBackground, nil
}

func probeTerminalBackground() (terminalRGB, bool) {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return terminalRGB{}, false
	}
	state, err := term.MakeRaw(in)
	if err != nil {
		return terminalRGB{}, false
	}
	defer func() { _ = term.Restore(in, state) }()
	if _, err = os.Stdout.Write([]byte("\x1b]11;?\x1b\\")); err != nil {
		return terminalRGB{}, false
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	response := make([]byte, 0, 64)
	var next [1]byte
	for time.Now().Before(deadline) && len(response) < 512 {
		remaining := int(time.Until(deadline).Milliseconds())
		if remaining < 1 {
			break
		}
		fds := []unix.PollFd{{Fd: int32(in), Events: unix.POLLIN}}
		if _, err = unix.Poll(fds, remaining); errors.Is(err, syscall.EINTR) {
			continue
		} else if err != nil || fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return terminalRGB{}, false
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, readErr := unix.Read(in, next[:])
		if readErr != nil {
			return terminalRGB{}, false
		}
		if n != 1 {
			continue
		}
		response = append(response, next[0])
		if color, complete := parseOSCBackground(response); complete {
			return color, true
		}
	}
	return terminalRGB{}, false
}

func parseOSCBackground(data []byte) (terminalRGB, bool) {
	start := bytes.Index(data, []byte("\x1b]11;"))
	if start < 0 {
		return terminalRGB{}, false
	}
	payloadStart := start + len("\x1b]11;")
	for end := payloadStart; end < len(data); end++ {
		terminator := 0
		if data[end] == 0x07 || data[end] == 0x9c {
			terminator = 1
		} else if data[end] == 0x1b && end+1 < len(data) && data[end+1] == '\\' {
			terminator = 2
		}
		if terminator == 0 {
			continue
		}
		color, ok := parseOSCColor(string(data[payloadStart:end]))
		return color, ok
	}
	return terminalRGB{}, false
}

func parseOSCColor(payload string) (terminalRGB, bool) {
	prefix, raw, ok := strings.Cut(strings.ToLower(payload), ":")
	if !ok || (prefix != "rgb" && prefix != "rgba") {
		return terminalRGB{}, false
	}
	parts := strings.Split(raw, "/")
	want := 3
	if prefix == "rgba" {
		want = 4
	}
	if len(parts) != want {
		return terminalRGB{}, false
	}
	values := make([]byte, want)
	for i, part := range parts {
		if len(part) < 1 || len(part) > 4 {
			return terminalRGB{}, false
		}
		value, err := strconv.ParseUint(part, 16, 16)
		if err != nil {
			return terminalRGB{}, false
		}
		maximum := uint64(1<<(4*len(part))) - 1
		values[i] = byte(value * 255 / maximum)
	}
	return terminalRGB{values[0], values[1], values[2]}, true
}
