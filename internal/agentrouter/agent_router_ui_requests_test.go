package agentrouter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/agentfake"
	"krystof.liskovec/jara/internal/protocol"
)

func TestAgentRouterAnswersCodexSideQuestionsInEphemeralFork(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	id := h.Request("jara/btw", map[string]any{"threadId": thread, "question": "what file?"})
	answer, seen := h.WaitFor(func(m map[string]any) bool { v, ok := m["id"].(float64); return ok && v == id })
	if got := answer["result"].(map[string]any)["answer"]; got != "codex: what file?" {
		t.Fatalf("side answer %v", got)
	}
	for _, m := range seen {
		if m["method"] != nil {
			t.Fatalf("fork notification leaked to the UI: %v", m)
		}
	}
	if fork := agentfake.ReadLog(t, "codex-fork"); len(fork) != 1 || fork[0] != thread+"|true|read-only" {
		t.Fatalf("fork request %q", fork)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if released := agentfake.ReadLog(t, "codex-unsubscribe"); len(released) == 1 && released[0] == "codex-fork" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the side-question fork was not released")
		}
	}
}

func TestAgentRouterAnswersClaudeSideQuestionsWithoutTools(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	h.Request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "hi"}}})
	h.WaitFor(agentfake.IsMethod("turn/completed"))
	reply := h.Response(h.Request("jara/btw", map[string]any{"threadId": thread, "question": "why?"}))
	if got := reply["result"].(map[string]any)["answer"]; got != "claude btw: why?" {
		t.Fatalf("side answer %v", reply)
	}
	args := agentfake.ReadLog(t, "claude-btw")
	if len(args) != 1 || !strings.Contains(args[0], "--tools  ") || !strings.Contains(args[0], "--resume "+thread+" --fork-session") {
		t.Fatalf("Claude side question must fork the session without tools: %q", args)
	}
}

func TestAgentRouterReadsAndSetsDefaultModels(t *testing.T) {
	agentfake.Setup(t)
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("JARA_FAKE_CODEX_MODEL", "gpt-configured")
	settings := filepath.Join(config, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"theme":"dark","model":"sonnet"}`), 0600); err != nil {
		t.Fatal(err)
	}
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	got := h.Response(h.Request("jara/defaultModel/get", map[string]any{"cwd": t.TempDir()}))["result"].(map[string]any)
	if got["agent"] != protocol.AgentClaude || got["codex"] != "gpt-configured" || got["claude"] != "claude/sonnet" {
		t.Fatalf("defaults %v", got)
	}
	if reply := h.Response(h.Request("jara/defaultModel/set", map[string]any{"model": "claude/opus"})); reply["error"] != nil {
		t.Fatalf("set Claude default: %v", reply)
	}
	data, _ := os.ReadFile(settings)
	var saved map[string]any
	if json.Unmarshal(data, &saved) != nil || saved["model"] != "opus" || saved["theme"] != "dark" {
		t.Fatalf("Claude settings %s", data)
	}
	if reply := h.Response(h.Request("jara/defaultModel/set", map[string]any{"model": "gpt-new"})); reply["error"] != nil {
		t.Fatalf("set Codex default: %v", reply)
	}
	if written := agentfake.ReadLog(t, "codex-config"); len(written) != 1 || written[0] != "model=gpt-new" {
		t.Fatalf("Codex config write %q", written)
	}
}

func TestDefaultModelChoosesTheStartingAgent(t *testing.T) {
	agentfake.Setup(t)
	if err := saveModelPrefs(modelPrefs{Default: "claude/opus"}); err != nil {
		t.Fatal(err)
	}
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))["result"].(map[string]any)
	if started["model"] != "claude/opus" || started["thread"].(map[string]any)["id"] == "codex-thread-1" {
		t.Fatalf("a Claude default must start Claude conversations: %v", started)
	}
	defaults := h.Response(h.Request("jara/defaultModel/get", map[string]any{}))["result"].(map[string]any)
	if defaults["agent"] != protocol.AgentClaude || defaults["default"] != "claude/opus" {
		t.Fatalf("defaults %v", defaults)
	}

	// An explicitly chosen agent still wins over the default model.
	r := &agentRouter{defaultAgent: protocol.AgentCodex, agentExplicit: true}
	if agent, model := r.startAgent(); agent != protocol.AgentCodex || model != "" {
		t.Fatalf("explicit agent: %s %s", agent, model)
	}
	if reply := h.Response(h.Request("jara/fallbackModel/set", map[string]any{"model": "gpt-x"})); reply["error"] != nil || LoadModelPrefs().Fallback != "gpt-x" {
		t.Fatalf("fallback not saved: %v", reply)
	}
}

func TestAgentRouterRunsClaudeInAutoMode(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir(), "approvalsReviewer": protocol.AutoReviewer}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	h.Request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "hi"}}})
	h.WaitFor(agentfake.IsMethod("turn/completed"))
	if args := agentfake.ReadLog(t, "claude-args"); len(args) != 1 || !strings.Contains(args[0], "--permission-mode auto") {
		t.Fatalf("auto mode must start Claude Code in its auto permission mode: %q", args)
	}
	h.Request("turn/start", map[string]any{"threadId": thread, "approvalsReviewer": "user", "input": []any{map[string]any{"type": "text", "text": "ask me"}}})
	h.WaitFor(agentfake.IsMethod("turn/completed"))
	if controls := strings.Join(agentfake.ReadLog(t, "claude-control"), ","); !strings.Contains(controls, "set_permission_mode <nil>default") {
		t.Fatalf("turning auto mode off must restore the default mode: %s", controls)
	}
	if routed := LoadThread(thread); routed == nil || routed.Reviewer != "user" {
		t.Fatalf("approvals reviewer was not saved: %+v", routed)
	}
}
