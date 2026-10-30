package jara

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type terminalRGB struct{ r, g, b byte }

var fallbackTerminalBackground = terminalRGB{38, 38, 38}

func configuredTerminalBackground() (terminalRGB, bool, error) {
	data, err := os.ReadFile("/run/jara/tmux-background")
	if errors.Is(err, os.ErrNotExist) {
		return terminalRGB{}, false, nil
	}
	if err != nil {
		return terminalRGB{}, false, fmt.Errorf("read tmux background setting: %w", err)
	}
	return parseTerminalBackground(strings.TrimSpace(string(data)))
}

func parseTerminalBackground(value string) (terminalRGB, bool, error) {
	if value == "" {
		return terminalRGB{}, false, nil
	}
	if len(value) != 7 || value[0] != '#' {
		return terminalRGB{}, false, fmt.Errorf("JARA_TMUX_BACKGROUND must be a six-digit RGB color such as #262626")
	}
	var color terminalRGB
	parts := []*byte{&color.r, &color.g, &color.b}
	for i, target := range parts {
		component, err := strconv.ParseUint(value[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return terminalRGB{}, false, fmt.Errorf("invalid JARA_TMUX_BACKGROUND %q", value)
		}
		*target = byte(component)
	}
	return color, true, nil
}

func terminalBackground() (terminalRGB, error) {
	if value := os.Getenv("JARA_TMUX_BACKGROUND"); value != "" {
		color, _, err := parseTerminalBackground(value)
		return color, err
	}
	color, configured, err := configuredTerminalBackground()
	if err != nil {
		return terminalRGB{}, err
	}
	if configured {
		return color, nil
	}
	return fallbackTerminalBackground, nil
}

func (c terminalRGB) hex() string { return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b) }

func (c terminalRGB) foreground() terminalRGB {
	if (299*int(c.r)+587*int(c.g)+114*int(c.b))/1000 > 128 {
		return terminalRGB{32, 32, 32}
	}
	return terminalRGB{208, 208, 208}
}

func (c terminalRGB) oscResponse(slot byte) []byte {
	return []byte(fmt.Sprintf("\x1b]%d;rgb:%04x/%04x/%04x\x1b\\", slot,
		uint16(c.r)*257, uint16(c.g)*257, uint16(c.b)*257))
}
