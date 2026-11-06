package jara

import "fmt"

func newestStableCodex(current string, latest func() (string, error)) (string, error) {
	if _, err := parseRelease(current); err != nil {
		return "", err
	}
	target, err := latest()
	if err != nil {
		return "", fmt.Errorf("check latest Codex (or pass --keep-codex-version): %w", err)
	}
	newer, err := newerRelease(target, current)
	if err != nil {
		return "", err
	}
	if !newer {
		return current, nil
	}
	return target, nil
}
