package jara

import (
	"fmt"
	"io"
)

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

// addMissingClaudePin adds Claude Code to guided deployments created before
// Jara supported it. An existing pin, including an empty "disabled" value, is
// preserved. A failed release lookup leaves the deployment Codex-only.
func addMissingClaudePin(settings map[string]string, latest func() (string, error), log io.Writer) bool {
	if _, present := settings["CLAUDE_CODE_VERSION"]; present {
		return false
	}
	version, err := latest()
	if err == nil {
		_, err = parseRelease(version)
	}
	if err != nil {
		fmt.Fprintln(log, "WARNING: Claude Code will not be installed; latest release lookup failed:", err)
		return false
	}
	settings["CLAUDE_CODE_VERSION"] = version
	fmt.Fprintf(log, "Claude Code %s will be installed alongside Codex.\n", version)
	return true
}

func appendEnvSetting(data []byte, key, value string) []byte {
	out := append([]byte{}, data...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, key+"="+value+"\n"...)
}
