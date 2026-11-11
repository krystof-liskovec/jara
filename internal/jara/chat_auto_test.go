package jara

import (
	"bytes"
	"strings"
	"testing"
)

func TestAgentRouterRunsClaudeInAutoMode(t *testing.T) {
	setupFakeAgents(t)
	h := startRouterHarness(t, agentClaude)
	h.response(h.request("initialize", map[string]any{}))
	started := h.response(h.request("thread/start", map[string]any{"cwd": t.TempDir(), "approvalsReviewer": chatAutoReviewer}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	h.request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "hi"}}})
	h.waitFor(isMethod("turn/completed"))
	if args := readFakeLog(t, "claude-args"); len(args) != 1 || !strings.Contains(args[0], "--permission-mode auto") {
		t.Fatalf("auto mode must start Claude Code in its auto permission mode: %q", args)
	}
	h.request("turn/start", map[string]any{"threadId": thread, "approvalsReviewer": "user", "input": []any{map[string]any{"type": "text", "text": "ask me"}}})
	h.waitFor(isMethod("turn/completed"))
	if controls := strings.Join(readFakeLog(t, "claude-control"), ","); !strings.Contains(controls, "set_permission_mode <nil>default") {
		t.Fatalf("turning auto mode off must restore the default mode: %s", controls)
	}
	if routed := loadRoutedThread(thread); routed == nil || routed.Reviewer != "user" {
		t.Fatalf("approvals reviewer was not saved: %+v", routed)
	}
}

func TestChatAutoCommandSelectsReviewer(t *testing.T) {
	t.Setenv("TMUX", "")
	var output bytes.Buffer
	u := &chatUI{out: &output}
	if u.approvalsReviewer() != chatAutoReviewer {
		t.Fatal("auto mode must be on by default")
	}
	u.runChatAutoCommand("/auto off")
	if u.approvalsReviewer() != "user" || !strings.Contains(output.String(), "Auto mode is off") {
		t.Fatalf("auto off: %q", output.String())
	}
	u.busy = true
	u.runChatAutoCommand("/auto")
	if u.approvalsReviewer() != chatAutoReviewer || !strings.Contains(output.String(), "applies from the next turn") {
		t.Fatalf("auto on: %q", output.String())
	}
	u.runChatAutoCommand("/auto maybe")
	if !strings.Contains(output.String(), "Use /auto, /auto on") || !isChatAutoCommand(" /auto status") || isChatAutoCommand("/automatic") {
		t.Fatalf("usage: %q", output.String())
	}
}
