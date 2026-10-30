//go:build !linux && !darwin

package jara

func detectTerminalBackground() (terminalRGB, error) {
	return terminalBackground()
}

func parseOSCBackground([]byte) (terminalRGB, bool) { return terminalRGB{}, false }

func parseOSCColor(string) (terminalRGB, bool) { return terminalRGB{}, false }
