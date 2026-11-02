//go:build linux || darwin

package jara

func menuTerminalBackground() (terminalRGB, error) {
	if color, ok := probeTerminalBackground(); ok {
		return color, nil
	}
	return terminalBackground()
}
