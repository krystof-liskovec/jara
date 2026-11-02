//go:build !linux && !darwin

package jara

func menuTerminalBackground() (terminalRGB, error) { return terminalBackground() }
