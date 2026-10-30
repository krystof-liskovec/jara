//go:build !linux && !darwin

package jara

import "os/exec"

func clipboardTerminal(r Remote, cmd *exec.Cmd) error { return interactive(cmd) }
