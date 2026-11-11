package jara

import (
	"os"
	"os/exec"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
)

// validateAgentInterface checks the runtime name. Both agents work in both
// interfaces: the Jara UI reaches Claude Code through the agent router.
func validateAgentInterface(runtime, userInterface string) error {
	_, err := protocol.NormalizeAgent(runtime)
	return err
}

func terminalBinaryAllowed(binary string) bool {
	return binary == protocol.CodexBinary || binary == protocol.ClaudeBinary
}

// claudeCommand builds an interactive Claude Code invocation. Picker resume
// maps to --resume and latest-session resume to --continue.
func claudeCommand(resume, latest bool, args []string) []string {
	command := []string{protocol.ClaudeBinary}
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
	command := []string{protocol.ClaudeBinary, "--print"}
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
		if q.Runtime == protocol.AgentClaude {
			return appendPrompt(claudeCommand(false, false, args), q.Prompt), nil
		}
		return appendPrompt(codexCommand(false, args), q.Prompt), nil
	}
	if q.Conversation != "" {
		return resumeConversationCommand(q.Agent, q.Conversation, q.ConversationCWD, args)
	}
	if q.Runtime != protocol.AgentClaude {
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
	runtimes := []string{protocol.AgentCodex}
	if _, err := exec.LookPath("claude"); err == nil {
		runtimes = append(runtimes, protocol.AgentClaude)
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
