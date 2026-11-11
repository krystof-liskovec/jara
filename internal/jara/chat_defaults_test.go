package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
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
	u.defaults = &protocol.DefaultModels{Agent: protocol.AgentCodex, Codex: "gpt-6.1-sol"}
	if got := u.modelControlLabel(); got != "gpt-6.1-sol" {
		t.Fatalf("new conversation label: %q", got)
	}
	u.defaults.Agent = protocol.AgentClaude
	if got := u.modelControlLabel(); got != "Claude Code" {
		t.Fatalf("unconfigured Claude default: %q", got)
	}
	u.defaults.Claude = "claude/claude-opus-5-5"
	if got := u.modelControlLabel(); got != "Claude Opus 5.5" {
		t.Fatalf("configured Claude default: %q", got)
	}
	u.setEffectiveModel(protocol.ClaudeDefaultModel)
	u.resolvedModel = "claude-sonnet-5-5"
	if got := u.modelControlLabel(); got != "Claude Sonnet 5.5" {
		t.Fatalf("resolved Claude model: %q", got)
	}
	u.setEffectiveModel("gpt-6.1-sol")
	if u.resolvedModel != "" || u.modelControlLabel() != "gpt-6.1-sol" {
		t.Fatalf("model change must drop the resolved Claude model: %q", u.modelControlLabel())
	}
}

func TestChatDefaultCommandSetsHostDefault(t *testing.T) {
	t.Setenv("TMUX", "")
	var output, sent bytes.Buffer
	u := &chatUI{out: &output, c: &appConnection{encoder: json.NewEncoder(&sent)}, defaults: &protocol.DefaultModels{Agent: protocol.AgentCodex, Codex: "gpt-old"}}
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
