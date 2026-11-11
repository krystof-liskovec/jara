package jara

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestChatNearestReasoningAcrossProviders(t *testing.T) {
	options := func(values ...string) *codexModel {
		model := &codexModel{DefaultReasoningEffort: "medium"}
		for _, value := range values {
			model.SupportedReasoningEfforts = append(model.SupportedReasoningEfforts, chatReasoningEffortOption{Value: value})
		}
		return model
	}
	claude, codex := options("low", "medium", "high", "xhigh", "max"), options("minimal", "low", "medium", "high", "xhigh")
	for _, c := range []struct {
		model        *codexModel
		effort, want string
	}{{claude, "minimal", "low"}, {codex, "max", "xhigh"}, {claude, "high", "high"}, {codex, "turbo", "medium"}, {options("low", "high"), "medium", "low"}} {
		if got := chatNearestReasoning(c.model, c.effort); got != c.want {
			t.Errorf("%s: got %s, want %s", c.effort, got, c.want)
		}
	}
}

func TestDefaultModelChoosesTheStartingAgent(t *testing.T) {
	setupFakeAgents(t)
	if err := saveModelPrefs(modelPrefs{Default: "claude/opus"}); err != nil {
		t.Fatal(err)
	}
	h := startRouterHarness(t, agentCodex)
	h.response(h.request("initialize", map[string]any{}))
	started := h.response(h.request("thread/start", map[string]any{"cwd": t.TempDir()}))["result"].(map[string]any)
	if started["model"] != "claude/opus" || started["thread"].(map[string]any)["id"] == "codex-thread-1" {
		t.Fatalf("a Claude default must start Claude conversations: %v", started)
	}
	defaults := h.response(h.request("jara/defaultModel/get", map[string]any{}))["result"].(map[string]any)
	if defaults["agent"] != agentClaude || defaults["default"] != "claude/opus" {
		t.Fatalf("defaults %v", defaults)
	}

	// An explicitly chosen agent still wins over the default model.
	r := &agentRouter{defaultAgent: agentCodex, agentExplicit: true}
	if agent, model := r.startAgent(); agent != agentCodex || model != "" {
		t.Fatalf("explicit agent: %s %s", agent, model)
	}
	if reply := h.response(h.request("jara/fallbackModel/set", map[string]any{"model": "gpt-x"})); reply["error"] != nil || loadModelPrefs().Fallback != "gpt-x" {
		t.Fatalf("fallback not saved: %v", reply)
	}
}

func TestCrunchContinuesOnTheFallbackModel(t *testing.T) {
	m, err := newChatCrunchManager(filepath.Join(t.TempDir(), "crunch.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.job = &chatCrunchJob{ID: "cr-1", ThreadID: "t1", Mode: "prompt", Prompt: "do it", Status: "running", Started: true, TurnID: "turn-1", Fallback: "claude/opus"}
	var input bytes.Buffer
	var mu sync.Mutex
	idle := func() bool { return false }
	m.handleAppMessage(appMessage{Method: "turn/completed", Params: json.RawMessage(`{"turn":{"id":"turn-1","status":"failed","error":{"message":"limit","codexErrorInfo":"usageLimitExceeded"}}}`)}, &input, &mu, idle)
	if m.job.Status != "pending" || !m.job.FellBack {
		t.Fatalf("crunch must continue on the fallback: %+v", m.job)
	}
	if err = m.tick(&input, &mu, idle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.String(), `"model":"claude/opus"`) {
		t.Fatalf("continuation must use the fallback model: %s", input.String())
	}
	m.job.Status, m.job.TurnID = "running", "turn-2"
	m.handleAppMessage(appMessage{Method: "turn/completed", Params: json.RawMessage(`{"turn":{"id":"turn-2","status":"failed","error":{"message":"limit","codexErrorInfo":"usageLimitExceeded"}}}`)}, &input, &mu, idle)
	if m.job.Status != "waitingUsage" {
		t.Fatalf("a second limit waits for the reset: %+v", m.job)
	}
}

func TestTurnRetriesOnTheFallbackModelOnce(t *testing.T) {
	t.Setenv("TMUX", "")
	var output, sent bytes.Buffer
	u := &chatUI{out: &output, c: &appConnection{encoder: json.NewEncoder(&sent)}, thread: "t1", threadReady: true,
		effectiveModel: "gpt-x", defaults: &defaultModels{Fallback: "claude/opus"}}
	if err := u.sendPrompt("fix the bug"); err != nil {
		t.Fatal(err)
	}
	u.busy = false
	limit := json.RawMessage(`{"turn":{"status":"failed","error":{"message":"You've hit your usage limit","codexErrorInfo":"usageLimitExceeded"}}}`)
	retried, err := u.retryOnFallback(limit)
	if err != nil || !retried || !strings.Contains(sent.String(), `"model":"claude/opus"`) || strings.Count(sent.String(), "fix the bug") != 2 {
		t.Fatalf("retry: %v %v %s", retried, err, sent.String())
	}
	u.busy = false
	if again, _ := u.retryOnFallback(limit); again {
		t.Fatal("a prompt is retried on the fallback only once")
	}
}
