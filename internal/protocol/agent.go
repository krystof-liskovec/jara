// Package protocol holds what Jara's interface and its agent router share:
// agent and model identity and the wire types of the app-server protocol.
package protocol

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
)

// Agent runtimes that Jara can launch in a managed tmux terminal. Codex is the
// default and the only runtime behind the built-in Jara conversation UI.
const (
	AgentCodex   = "codex"
	AgentClaude  = "claude"
	CodexBinary  = "/usr/local/bin/codex"
	ClaudeBinary = "/usr/local/bin/claude"
)

func NormalizeAgent(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", AgentCodex:
		return AgentCodex, nil
	case AgentClaude, "claude-code":
		return AgentClaude, nil
	default:
		return "", fmt.Errorf("--agent must be codex or claude")
	}
}

func AgentDisplayName(runtime string) string {
	if runtime == AgentClaude {
		return "Claude Code"
	}
	return "Codex"
}

func AgentForModel(model string) string {
	if strings.HasPrefix(model, ClaudeModelPrefix) {
		return AgentClaude
	}
	return AgentCodex
}

func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

const ModelMaxIDBytes = 128

// CodexExecutable allows tests to substitute a fake CLI.
func CodexExecutable() string {
	if path := os.Getenv("JARA_CODEX_BIN"); path != "" {
		return path
	}
	return CodexBinary
}

// ClaudeExecutable allows tests to substitute a fake CLI.
func ClaudeExecutable() string {
	if path := os.Getenv("JARA_CLAUDE_BIN"); path != "" {
		return path
	}
	return ClaudeBinary
}

func ClaudeAvailable() bool {
	_, err := os.Stat(ClaudeExecutable())
	return err == nil
}
