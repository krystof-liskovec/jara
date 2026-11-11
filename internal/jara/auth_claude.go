package jara

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
	"golang.org/x/term"
)

// claudeTokenDestination is the fixed destination argument for `jara _auth
// claude`; the token is stored for the Claude Code launcher, not a Git host.
const claudeTokenDestination = "anthropic"

func claudeTokenPath() string {
	return filepath.Join(fsutil.DataRoot(), "auth", "claude-token")
}

// validateClaudeToken accepts long-lived OAuth tokens from `claude setup-token`
// (sk-ant-oat…) and Anthropic API keys (sk-ant-api…).
func validateClaudeToken(token string) error {
	if !strings.HasPrefix(token, "sk-ant-") || strings.ContainsAny(token, " \t\r\n\x00'\"\\$`") {
		return fmt.Errorf("expected a Claude token beginning with sk-ant-; generate one with `claude setup-token`")
	}
	return nil
}

// claudeAuthCommand reads a token locally and stores it in the remote agent
// home. Tokens travel over SSH standard input and never appear in arguments.
func claudeAuthCommand(remote Remote, tokenStdin bool) error {
	var secret []byte
	var err error
	if tokenStdin {
		secret, err = io.ReadAll(io.LimitReader(os.Stdin, 65537))
		if err != nil {
			return err
		}
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("no terminal for a hidden prompt; pipe the token with --token-stdin")
		}
		fmt.Fprintln(os.Stderr, "Generate a long-lived Claude token by running `claude setup-token` on any machine with a browser.")
		fmt.Fprintln(os.Stderr, "An Anthropic API key (sk-ant-api…) is also accepted. The token is stored only in the remote Jara home.")
		fmt.Fprint(os.Stderr, "Claude token (input hidden): ")
		secret, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
	}
	secret = bytes.TrimSpace(secret)
	if err = validateClaudeToken(string(secret)); err != nil {
		return err
	}
	cmd := sshCommand(remote, false, shellArgs("jara", "_auth", protocol.AgentClaude, claudeTokenDestination))
	cmd.Stdin = bytes.NewReader(secret)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("store Claude token on remote: %w", err)
	}
	return nil
}

// storeClaudeToken writes the token atomically with owner-only permissions.
func storeClaudeToken(secret []byte) error {
	token := string(secret)
	if err := validateClaudeToken(token); err != nil {
		return err
	}
	path := claudeTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := writePrivateFile(path, []byte(token+"\n")); err != nil {
		return err
	}
	if err := markClaudeOnboarded(); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: could not mark Claude Code onboarding complete:", err)
	}
	fmt.Fprintln(os.Stderr, "Claude token configured; new Claude Code sessions will use it.")
	return nil
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".jara-private-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if e := file.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func claudeConfigPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// markClaudeOnboarded skips Claude Code's first-run login wizard, which would
// otherwise ask for a browser login even though a token is supplied. Existing
// settings are preserved; an unreadable file is left untouched.
func markClaudeOnboarded() error {
	path := claudeConfigPath()
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s is not valid JSON; left unchanged", path)
		}
		if done, _ := settings["hasCompletedOnboarding"].(bool); done {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	settings["hasCompletedOnboarding"] = true
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writePrivateFile(path, append(encoded, '\n'))
}

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
