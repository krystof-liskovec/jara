package jara

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

// claudeTokenDestination is the fixed destination argument for `jara _auth
// claude`; the token is stored for the Claude Code launcher, not a Git host.
const claudeTokenDestination = "anthropic"

func claudeTokenPath() string { return agentrouter.ClaudeTokenPath() }

func validateClaudeToken(token string) error { return agentrouter.ValidateClaudeToken(token) }

// claudeAuthCommand reads a token locally and stores it in the remote agent
// home. Tokens travel over SSH standard input and never appear in arguments.
func claudeAuthCommand(remote Remote, tokenStdin bool) error {
	secret, err := readClaudeSecret(tokenStdin, "The token is stored only in the remote Jara home.")
	if err != nil {
		return err
	}
	if err = sendClaudeToken(remote, secret); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Claude token configured on the remote; new Claude Code sessions there use it.")
	return nil
}

// storeClaudeToken writes the token with owner-only permissions.
func storeClaudeToken(secret []byte) error {
	if err := agentrouter.StoreClaudeToken(string(secret)); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Claude token configured; new Claude Code sessions will use it.")
	return nil
}

func writePrivateFile(path string, data []byte) error { return fsutil.WritePrivateFile(path, data) }

func claudeConfigPath() string { return agentrouter.ClaudeConfigPath() }

func markClaudeOnboarded() error { return agentrouter.MarkClaudeOnboarded() }

// claudeCredentialsPresent recognizes a Jara-stored token, an inherited
// environment token, or credentials from an interactive `claude` login.
func claudeCredentialsPresent() bool {
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true
	}
	if info, err := os.Stat(claudeTokenPath()); err == nil && info.Size() > 0 {
		return true
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	info, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil && info.Size() > 0
}

// offerClaudeSignIn is part of guided setup. Claude Code is optional, so
// declining or an older host without Claude Code never fails setup.
func offerClaudeSignIn(c Config, name string, d Diagnosis, reader *bufio.Reader) error {
	if !claudeInstalled(d) {
		return nil
	}
	if !claudeAuthenticationMissing(d) {
		fmt.Println("Claude Code is already signed in on this Jara host.")
		return nil
	}
	yes, err := askYes(reader, "Also configure Claude Code with a token from `claude setup-token`?", false)
	if err != nil {
		return err
	}
	if !yes {
		fmt.Printf("You can add it later with `jara auth claude --remote %s`.\n", name)
		return nil
	}
	return authCommand(c, []string{protocol.AgentClaude, "--remote", name})
}
