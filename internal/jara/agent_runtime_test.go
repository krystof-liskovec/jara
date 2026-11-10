package jara

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeAgentRuntime(t *testing.T) {
	for input, want := range map[string]string{"": agentCodex, "codex": agentCodex, "Claude": agentClaude, "claude-code": agentClaude} {
		got, err := normalizeAgentRuntime(input)
		if err != nil || got != want {
			t.Fatalf("normalizeAgentRuntime(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := normalizeAgentRuntime("gemini"); err == nil {
		t.Fatal("unknown runtimes must be rejected")
	}
	if err := validateAgentInterface(agentClaude, "jara"); err != nil {
		t.Fatalf("Claude Code is served to the Jara UI by the agent router: %v", err)
	}
	if err := validateAgentInterface(agentClaude, "codex"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentTerminalCommands(t *testing.T) {
	args := []string{"--model", "opus"}
	cases := []struct {
		q    Request
		want []string
	}{
		{Request{}, []string{codexBinary, "--model", "opus"}},
		{Request{Runtime: agentClaude}, []string{claudeBinary, "--model", "opus"}},
		{Request{Runtime: agentClaude, Resume: true}, []string{claudeBinary, "--resume", "--model", "opus"}},
		{Request{Runtime: agentClaude, ResumeLatest: true}, []string{claudeBinary, "--continue", "--model", "opus"}},
	}
	for _, c := range cases {
		got, err := agentTerminalCommand(c.q, args)
		if err != nil || !slices.Equal(got, c.want) {
			t.Fatalf("%+v: got %q, %v; want %q", c.q, got, err, c.want)
		}
	}
	t.Setenv("JARA_DATA", t.TempDir())
	if got, err := agentTerminalCommand(Request{Runtime: agentClaude, Agent: "Codex", Conversation: "thread"}, nil); err != nil || got[0] != codexBinary || got[1] != "resume" {
		t.Fatalf("a saved Codex conversation resumes in Codex: %q %v", got, err)
	}
	if !terminalBinaryAllowed(claudeBinary) || terminalBinaryAllowed("/bin/sh") {
		t.Fatal("terminal relay allow-list is wrong")
	}
}

func TestBackgroundClaudeKeepsPromptOutOfArguments(t *testing.T) {
	q := Request{Runtime: agentClaude, ResumeLatest: true, Prompt: "--dangerous prompt", CodexArgs: []string{"--permission-mode", "acceptEdits"}}
	got := backgroundClaudeCommand(q)
	want := []string{claudeBinary, "--print", "--continue", "--permission-mode", "acceptEdits"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClaudeSessionLabels(t *testing.T) {
	if got := sessionMenuLabel(Request{Runtime: agentClaude}); got != "Claude Code" {
		t.Fatalf("label %q", got)
	}
	if got := sessionMenuLabel(Request{Runtime: agentClaude, ResumeLatest: true}); got != "Resume latest Claude Code" {
		t.Fatalf("label %q", got)
	}
	if got := sessionMenuLabel(Request{}); got != "" {
		t.Fatalf("Codex default label changed: %q", got)
	}
}

func TestValidateClaudeToken(t *testing.T) {
	for _, ok := range []string{"sk-ant-oat01-abcDEF_123-xyz", "sk-ant-api03-key"} {
		if err := validateClaudeToken(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ghp_token", "sk-ant-oat01 space", "sk-ant-$(id)", "sk-ant-'x'"} {
		if validateClaudeToken(bad) == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

func TestStoreClaudeTokenAndOnboarding(t *testing.T) {
	data, config := t.TempDir(), t.TempDir()
	t.Setenv("JARA_DATA", data)
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	existing := []byte(`{"theme":"dark","projects":{}}`)
	if err := os.WriteFile(filepath.Join(config, ".claude.json"), existing, 0600); err != nil {
		t.Fatal(err)
	}
	if claudeCredentialsPresent() {
		t.Fatal("no credentials should be reported before storing a token")
	}
	if err := storeClaudeToken([]byte("sk-ant-oat01-secret")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(claudeTokenPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token file mode: %v, %v", info, err)
	}
	if b, _ := os.ReadFile(claudeTokenPath()); string(b) != "sk-ant-oat01-secret\n" {
		t.Fatalf("token content %q", b)
	}
	var settings map[string]any
	b, _ := os.ReadFile(filepath.Join(config, ".claude.json"))
	if err = json.Unmarshal(b, &settings); err != nil || settings["hasCompletedOnboarding"] != true || settings["theme"] != "dark" {
		t.Fatalf("onboarding settings not merged: %s", b)
	}
	if !claudeCredentialsPresent() {
		t.Fatal("stored token not detected")
	}
	if err = os.WriteFile(filepath.Join(config, ".claude.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if markClaudeOnboarded() == nil {
		t.Fatal("invalid JSON should be reported and left untouched")
	}
	if b, _ = os.ReadFile(filepath.Join(config, ".claude.json")); string(b) != "{broken" {
		t.Fatal("invalid Claude settings were overwritten")
	}
}

func TestRemoteAuthStoresClaudeToken(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if err := remoteAuth(agentClaude, "elsewhere"); err == nil {
		t.Fatal("unexpected destinations must be rejected")
	}
	stdin := os.Stdin
	defer func() { os.Stdin = stdin }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	go func() { _, _ = io.WriteString(writer, "  sk-ant-oat01-piped\n"); writer.Close() }()
	if err = remoteAuth(agentClaude, claudeTokenDestination); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(claudeTokenPath()); string(b) != "sk-ant-oat01-piped\n" {
		t.Fatalf("stored %q", b)
	}
}

func TestClaudeLauncherExportsStoredToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launcher")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s' \"$CLAUDE_CODE_OAUTH_TOKEN\" \"$ANTHROPIC_API_KEY\" \"$DISABLE_AUTOUPDATER\" \"$*\"\n"
	if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../deploy/claude-launcher.sh")
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launcher")
	patched := bytes.ReplaceAll(source, []byte("/opt/jara/claude-cli"), []byte(fake))
	if err = os.WriteFile(launcher, patched, 0755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	run := func(token string, env ...string) string {
		_ = os.MkdirAll(filepath.Join(data, "auth"), 0700)
		_ = os.WriteFile(filepath.Join(data, "auth", "claude-token"), []byte(token+"\n"), 0600)
		cmd := exec.Command(launcher, "--continue")
		cmd.Env = append([]string{"PATH=/usr/bin:/bin", "JARA_DATA=" + data}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("launcher: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := run("sk-ant-oat01-a"); got != "sk-ant-oat01-a||1|--continue" {
		t.Fatalf("OAuth token: %q", got)
	}
	if got := run("sk-ant-api03-b"); got != "|sk-ant-api03-b|1|--continue" {
		t.Fatalf("API key: %q", got)
	}
	if got := run("sk-ant-oat01-a", "CLAUDE_CODE_OAUTH_TOKEN=explicit"); got != "explicit||1|--continue" {
		t.Fatalf("explicit environment must win: %q", got)
	}
}

func TestClaudeDeploymentPins(t *testing.T) {
	settings := map[string]string{}
	var log bytes.Buffer
	if !addMissingClaudePin(settings, func() (string, error) { return "2.3.4", nil }, &log) || settings["CLAUDE_CODE_VERSION"] != "2.3.4" {
		t.Fatalf("missing pin not added: %v", settings)
	}
	disabled := map[string]string{"CLAUDE_CODE_VERSION": ""}
	if addMissingClaudePin(disabled, func() (string, error) { return "2.3.4", nil }, &log) || disabled["CLAUDE_CODE_VERSION"] != "" {
		t.Fatal("an explicit empty pin must stay disabled")
	}
	if got := string(appendEnvSetting([]byte("A=1"), "CLAUDE_CODE_VERSION", "2.3.4")); got != "A=1\nCLAUDE_CODE_VERSION=2.3.4\n" {
		t.Fatalf("env append %q", got)
	}
	args := nativeBuildArgs(map[string]string{"CLAUDE_CODE_VERSION": "2.3.4"}, "image", "")
	if !slices.Contains(args, "CLAUDE_CODE_VERSION=2.3.4") {
		t.Fatalf("build args omit Claude Code: %q", args)
	}
	if v, err := resolveClaudeCodeVersion("none"); err != nil || v != "" {
		t.Fatalf("none: %q, %v", v, err)
	}
	if _, err := resolveClaudeCodeVersion("latest-ish"); err == nil {
		t.Fatal("invalid versions must be rejected")
	}
}
