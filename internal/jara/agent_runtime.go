package jara

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Agent runtimes that Jara can launch in a managed tmux terminal. Codex is the
// default and the only runtime behind the built-in Jara conversation UI.
const (
	agentCodex   = "codex"
	agentClaude  = "claude"
	codexBinary  = "/usr/local/bin/codex"
	claudeBinary = "/usr/local/bin/claude"
)

func normalizeAgentRuntime(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", agentCodex:
		return agentCodex, nil
	case agentClaude, "claude-code":
		return agentClaude, nil
	default:
		return "", fmt.Errorf("--agent must be codex or claude")
	}
}

func agentDisplayName(runtime string) string {
	if runtime == agentClaude {
		return "Claude Code"
	}
	return "Codex"
}

// validateAgentInterface checks the runtime name. Both agents work in both
// interfaces: the Jara UI reaches Claude Code through the agent router.
func validateAgentInterface(runtime, userInterface string) error {
	_, err := normalizeAgentRuntime(runtime)
	return err
}

func terminalBinaryAllowed(binary string) bool {
	return binary == codexBinary || binary == claudeBinary
}

// claudeCommand builds an interactive Claude Code invocation. Picker resume
// maps to --resume and latest-session resume to --continue.
func claudeCommand(resume, latest bool, args []string) []string {
	command := []string{claudeBinary}
	if latest {
		command = append(command, "--continue")
	} else if resume {
		command = append(command, "--resume")
	}
	return append(command, args...)
}

// backgroundClaudeCommand runs Claude Code non-interactively. The prompt is
// supplied on standard input so it can never be parsed as an option.
func backgroundClaudeCommand(q Request) []string {
	command := []string{claudeBinary, "--print"}
	if q.ResumeLatest {
		command = append(command, "--continue")
	}
	return append(command, q.CodexArgs...)
}

func runBackgroundClaude(q Request) error {
	command := backgroundClaudeCommand(q)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(q.Prompt)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// agentTerminalCommand selects the interactive command for a new tmux session.
func agentTerminalCommand(q Request, args []string) ([]string, error) {
	if q.Handoff {
		// The handoff prompt is the final positional argument for both CLIs.
		if q.Runtime == agentClaude {
			return appendPrompt(claudeCommand(false, false, args), q.Prompt), nil
		}
		return appendPrompt(codexCommand(false, args), q.Prompt), nil
	}
	if q.Conversation != "" {
		return resumeConversationCommand(q.Agent, q.Conversation, q.ConversationCWD, args)
	}
	if q.Runtime != agentClaude {
		command := codexCommand(q.Resume, args)
		if q.ResumeLatest {
			command = codexResumeLatestCommand(args)
		}
		return command, nil
	}
	return claudeCommand(q.Resume, q.ResumeLatest, args), nil
}

// claudeDiagnostics reports optional Claude Code readiness without failing
// hosts that only use Codex.
func claudeDiagnostics() []string {
	if _, err := exec.LookPath("claude"); err != nil {
		return []string{"INFO Claude Code not installed: set CLAUDE_CODE_VERSION in the deployment settings and rebuild, or run jara deployment upgrade"}
	}
	if claudeCredentialsPresent() {
		return []string{"OK Claude Code", "OK Claude Code authentication"}
	}
	return []string{"OK Claude Code", "SETUP Claude Code authentication: run claude setup-token locally, then jara auth claude --remote NAME"}
}

func claudeAuthenticationMissing(d Diagnosis) bool {
	for _, check := range d.Checks {
		if strings.HasPrefix(check, "SETUP Claude Code authentication:") {
			return true
		}
	}
	return false
}

func claudeInstalled(d Diagnosis) bool {
	for _, check := range d.Checks {
		if check == "OK Claude Code" {
			return true
		}
	}
	return false
}

// installedAgentRuntimes lists runtimes whose CLI exists on this host.
func installedAgentRuntimes() []string {
	runtimes := []string{agentCodex}
	if _, err := exec.LookPath("claude"); err == nil {
		runtimes = append(runtimes, agentClaude)
	}
	return runtimes
}

// appendPrompt adds a positional prompt after "--" so variadic options such as
// Claude Code's --add-dir cannot consume it. If the user already ended option
// parsing, the prompt simply follows their arguments.
func appendPrompt(command []string, prompt string) []string {
	for _, arg := range command[1:] {
		if arg == "--" {
			return append(command, prompt)
		}
	}
	return append(command, "--", prompt)
}
