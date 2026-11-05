//go:build !linux && !darwin

package jara

import (
	"fmt"
	"os"
	"os/exec"
)

func startChatDaemonProcess(Request, string) (int, error) {
	return 0, fmt.Errorf("persistent Jara sessions require a Unix remote host")
}

func chatProcessExists(int) bool { return false }

func chatDaemonTerminationSignals() []os.Signal { return []os.Signal{os.Interrupt} }

func isolatePersistentChatProcess(*exec.Cmd) {}

func killPersistentChatProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
