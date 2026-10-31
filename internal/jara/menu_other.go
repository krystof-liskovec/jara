//go:build !unix

package jara

func terminalMenu(title string, items []menuItem) (int, bool, error) { return 0, false, nil }
