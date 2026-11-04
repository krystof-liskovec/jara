//go:build linux || darwin

package jara

import (
	"os/exec"
	"syscall"
)

// Only the Jara interface receives terminal Ctrl-C; it interrupts a turn via RPC.
func isolateChatProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
