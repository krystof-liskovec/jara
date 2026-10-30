//go:build !linux

package jag

func terminalMenu(title string, items []menuItem) (int, bool, error) { return 0, false, nil }
