//go:build linux

package jara

import (
	"os/exec"
	"syscall"
)

func isolatePersistentChatProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

func killPersistentChatProcess(cmd *exec.Cmd) {
	if cmd.Process == nil || syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}
