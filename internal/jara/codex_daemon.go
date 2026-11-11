package jara

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

const codexNoDaemonFlag = "--no-daemon"

// codexWithoutSharedDaemon keeps a Codex terminal's conversation inside its own
// process. Codex 0.167 otherwise runs terminals through a shared background
// app-server that keeps each thread's writer lease after the terminal exits,
// so Jara's own app-server could no longer resume that conversation.
func codexWithoutSharedDaemon(command []string) []string {
	if len(command) == 0 || command[0] != protocol.CodexBinary || !codexSupportsNoDaemon(command[0]) {
		return command
	}
	for _, arg := range command[1:] {
		if arg == codexNoDaemonFlag {
			return command
		}
	}
	return append([]string{command[0], codexNoDaemonFlag}, command[1:]...)
}

// codexSupportsNoDaemon is a variable so tests can avoid running Codex.
var codexSupportsNoDaemon = func(binary string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	help, err := exec.CommandContext(ctx, binary, "--help").Output()
	return err == nil && strings.Contains(string(help), codexNoDaemonFlag)
}

// chatWriterConflict reports Codex's rejection of a thread another process
// still writes to.
func chatWriterConflict(detail string) bool {
	return strings.Contains(strings.ToLower(detail), "active writer")
}

const chatWriterConflictHint = "Another Codex process still has this conversation open. Close its Codex terminal, " +
	"or if none is open, stop Codex's shared background server on the host with `codex app-server daemon stop`.\n"
