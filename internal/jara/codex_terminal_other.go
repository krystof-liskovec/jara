//go:build !linux && !darwin

package jara

import (
	"fmt"
	"os"
	"os/exec"
)

func runCodexTerminal(args []string) error {
	if len(args) == 0 || !terminalBinaryAllowed(args[0]) {
		return fmt.Errorf("invalid agent terminal command")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
