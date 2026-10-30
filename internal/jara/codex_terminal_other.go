//go:build !linux && !darwin

package jara

import (
	"fmt"
	"os"
	"os/exec"
)

func runCodexTerminal(args []string) error {
	if len(args) == 0 || args[0] != "/usr/local/bin/codex" {
		return fmt.Errorf("invalid Codex terminal command")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
