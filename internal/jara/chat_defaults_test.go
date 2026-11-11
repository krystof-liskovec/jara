package jara

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeModelPrettyName(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5": "Claude Opus 5.5", "claude-sonnet-4-5-20250929": "Claude Sonnet 4.5",
		"claude-fable-5-2[1m]": "Claude Fable 5.2 (1M)", "opus": "Claude Opus",
	} {
		if got := claudeModelPrettyName(id); got != want {
			t.Errorf("%s: got %q, want %q", id, got, want)
		}
	}
}

func TestModelControlNamesConcreteModels(t *testing.T) {
	u := &chatUI{}
	if got := u.modelControlLabel(); got != "Loading…" {
		t.Fatalf("before defaults load: %q", got)
	}
	u.defaults = &defaultModels{Agent: agentCodex, Codex: "gpt-6.1-sol"}
	if got := u.modelControlLabel(); got != "gpt-6.1-sol" {
		t.Fatalf("new conversation label: %q", got)
	}
	u.defaults.Agent = agentClaude
	if got := u.modelControlLabel(); got != "Claude Code" {
		t.Fatalf("unconfigured Claude default: %q", got)
	}
	u.defaults.Claude = "claude/claude-opus-5-5"
	if got := u.modelControlLabel(); got != "Claude Opus 5.5" {
		t.Fatalf("configured Claude default: %q", got)
	}
	u.setEffectiveModel(claudeDefaultModel)
	u.resolvedModel = "claude-sonnet-5-5"
	if got := u.modelControlLabel(); got != "Claude Sonnet 5.5" {
		t.Fatalf("resolved Claude model: %q", got)
	}
	u.setEffectiveModel("gpt-6.1-sol")
	if u.resolvedModel != "" || u.modelControlLabel() != "gpt-6.1-sol" {
		t.Fatalf("model change must drop the resolved Claude model: %q", u.modelControlLabel())
	}
}

func TestAgentRouterReadsAndSetsDefaultModels(t *testing.T) {
	setupFakeAgents(t)
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("JARA_FAKE_CODEX_MODEL", "gpt-configured")
	settings := filepath.Join(config, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"theme":"dark","model":"sonnet"}`), 0600); err != nil {
		t.Fatal(err)
	}
	h := startRouterHarness(t, agentClaude)
	h.response(h.request("initialize", map[string]any{}))
	got := h.response(h.request("jara/defaultModel/get", map[string]any{"cwd": t.TempDir()}))["result"].(map[string]any)
	if got["agent"] != agentClaude || got["codex"] != "gpt-configured" || got["claude"] != "claude/sonnet" {
		t.Fatalf("defaults %v", got)
	}
	if reply := h.response(h.request("jara/defaultModel/set", map[string]any{"model": "claude/opus"})); reply["error"] != nil {
		t.Fatalf("set Claude default: %v", reply)
	}
	data, _ := os.ReadFile(settings)
	var saved map[string]any
	if json.Unmarshal(data, &saved) != nil || saved["model"] != "opus" || saved["theme"] != "dark" {
		t.Fatalf("Claude settings %s", data)
	}
	if reply := h.response(h.request("jara/defaultModel/set", map[string]any{"model": "gpt-new"})); reply["error"] != nil {
		t.Fatalf("set Codex default: %v", reply)
	}
	if written := readFakeLog(t, "codex-config"); len(written) != 1 || written[0] != "model=gpt-new" {
		t.Fatalf("Codex config write %q", written)
	}
}

func TestChatDefaultCommandSetsHostDefault(t *testing.T) {
	t.Setenv("TMUX", "")
	var output, sent bytes.Buffer
	u := &chatUI{out: &output, c: &appConnection{encoder: json.NewEncoder(&sent)}, defaults: &defaultModels{Agent: agentCodex, Codex: "gpt-old"}}
	if err := u.runChatDefaultCommand("/default gpt-new"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.String(), `"method":"jara/defaultModel/set"`) || !strings.Contains(sent.String(), `"model":"gpt-new"`) {
		t.Fatalf("request %s", sent.String())
	}
	for id, pending := range u.pending {
		u.acceptChatDefaultResponse(appMessage{ID: json.RawMessage(id), Result: json.RawMessage(`{}`)}, pending)
	}
	if u.defaults.Codex != "gpt-new" || u.modelControlLabel() != "gpt-new" || !strings.Contains(output.String(), "Default model for new conversations: gpt-new (Codex)") {
		t.Fatalf("default not applied: %+v %q", u.defaults, output.String())
	}
	if !isChatDefaultCommand("/default") || isChatDefaultCommand("/defaults") {
		t.Fatal("command detection")
	}
}
