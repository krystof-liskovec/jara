package agentrouter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
)

// Claude Code signs in three ways: its own login (credentials in its config
// directory), CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY in the
// environment, or a token Jara stores, which Jara exports as one of those
// when it starts Claude Code and the environment sets neither.

func ClaudeTokenPath() string {
	return filepath.Join(fsutil.DataRoot(), "auth", "claude-token")
}

// ValidateClaudeToken accepts long-lived OAuth tokens from `claude
// setup-token` (sk-ant-oat…) and Anthropic API keys (sk-ant-api…).
func ValidateClaudeToken(token string) error {
	if !strings.HasPrefix(token, "sk-ant-") || strings.ContainsAny(token, " \t\r\n\x00'\"\\$`") {
		return errors.New("expected a Claude token beginning with sk-ant-; generate one with `claude setup-token`, or use an Anthropic API key")
	}
	return nil
}

// StoreClaudeToken saves token for Claude Code on this machine and skips its
// first-run wizard, which would otherwise ask for a browser login.
func StoreClaudeToken(token string) error {
	if err := ValidateClaudeToken(token); err != nil {
		return err
	}
	RememberAccounts() // the account used until now stays in the pool
	if err := writeClaudeToken(token); err != nil {
		return err
	}
	RememberAccounts()
	if err := MarkClaudeOnboarded(); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: could not mark Claude Code onboarding complete:", err)
	}
	return nil
}

func writeClaudeToken(token string) error {
	path := ClaudeTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return fsutil.WritePrivateFile(path, []byte(token+"\n"))
}

// claudeEnvToken names the variable a stored token is exported as.
func claudeEnvToken(token string) string {
	if strings.HasPrefix(token, "sk-ant-api") {
		return "ANTHROPIC_API_KEY"
	}
	return "CLAUDE_CODE_OAUTH_TOKEN"
}

// claudeEnv adds the stored token to env when env sets neither variable.
func claudeEnv(env []string) []string {
	for _, entry := range env {
		if name, value, _ := strings.Cut(entry, "="); (name == "CLAUDE_CODE_OAUTH_TOKEN" || name == "ANTHROPIC_API_KEY") && value != "" {
			return env
		}
	}
	data, err := os.ReadFile(ClaudeTokenPath())
	token := strings.TrimSpace(string(data))
	if err != nil || ValidateClaudeToken(token) != nil {
		return env
	}
	return append(env, claudeEnvToken(token)+"="+token)
}

func ClaudeConfigPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

func claudeCredentialsFile() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// claudeCredentialStamp changes whenever a sign-in changes, so a running
// Claude Code restarts to use the new one.
func claudeCredentialStamp() string {
	var stamp strings.Builder
	for _, path := range []string{ClaudeTokenPath(), claudeCredentialsFile()} {
		if info, err := os.Stat(path); err == nil {
			stamp.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10) + "/" + strconv.FormatInt(info.Size(), 10))
		}
		stamp.WriteString(";")
	}
	return stamp.String()
}

// MarkClaudeOnboarded skips Claude Code's first-run login wizard. Existing
// settings are preserved; an unreadable file is left untouched.
func MarkClaudeOnboarded() error {
	path := ClaudeConfigPath()
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
	return fsutil.WritePrivateFile(path, append(encoded, '\n'))
}
