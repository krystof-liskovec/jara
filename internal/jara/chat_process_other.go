//go:build !linux && !darwin

package jara

import "os/exec"

// Conversation processes run on Linux Jara hosts.
func isolateChatProcess(cmd *exec.Cmd) {}
