package agentrouter

import (
	"encoding/json"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/agentfake"
	"krystof.liskovec/jara/internal/protocol"
)

func TestAgentRouterSwitchesBetweenCodexAndClaude(t *testing.T) {
	agentfake.Setup(t)
	cwd := t.TempDir()
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	models := h.Response(h.Request("model/list", map[string]any{}))
	encoded, _ := json.Marshal(models)
	if !strings.Contains(string(encoded), `"gpt-x"`) || !strings.Contains(string(encoded), `"claude/opus"`) {
		t.Fatalf("model list must offer both agents: %s", encoded)
	}
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": cwd}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	if thread != "codex-thread-1" {
		t.Fatalf("thread %q", thread)
	}

	h.Request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "hello"}}})
	_, seen := h.WaitFor(agentfake.IsMethod("turn/completed"))
	if !strings.Contains(agentfake.AgentMessages(seen), "codex: hello") {
		t.Fatalf("codex turn: %v", seen)
	}

	// Picking a Claude model moves the conversation to Claude Code.
	id := h.Request("turn/start", map[string]any{"threadId": thread, "model": "claude/sonnet", "input": []any{map[string]any{"type": "text", "text": "continue please"}}})
	_, seen = h.WaitFor(agentfake.IsMethod("turn/completed"))
	if !strings.Contains(agentfake.AgentMessages(seen), "claude: continue please") {
		t.Fatalf("claude turn: %v", seen)
	}
	if ids := agentfake.ThreadIDs(seen); len(ids) != 1 || !ids[thread] {
		t.Fatalf("UI thread ID must stay stable: %v", ids)
	}
	if !agentfake.HasResponse(seen, id) {
		t.Fatal("turn/start must be answered")
	}
	prompts := agentfake.ReadLog(t, "claude-prompt")
	if len(prompts) != 1 || !strings.Contains(prompts[0], "started with Codex") || !strings.Contains(prompts[0], "User: hello") || !strings.Contains(prompts[0], "Codex: codex: hello") {
		t.Fatalf("switch briefing: %q", prompts)
	}
	if args := agentfake.ReadLog(t, "claude-args"); len(args) != 1 || !strings.Contains(args[0], "--model sonnet") || !strings.Contains(args[0], "--session-id") {
		t.Fatalf("claude args: %q", args)
	}

	// A Claude edit asks for approval and reports a diff for the pane.
	h.Request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "EDIT now"}}})
	approval, _ := h.WaitFor(agentfake.IsMethod("item/fileChange/requestApproval"))
	h.Send(map[string]any{"id": approval["id"], "result": map[string]any{"decision": "accept"}})
	_, seen = h.WaitFor(agentfake.IsMethod("turn/completed"))
	if got := agentfake.ReadLog(t, "claude-permission"); len(got) != 1 || got[0] != "allow" {
		t.Fatalf("permission answer: %v", got)
	}
	diff := ""
	for _, m := range seen {
		if m["method"] == "item/completed" {
			item := m["params"].(map[string]any)["item"].(map[string]any)
			if item["type"] == "fileChange" {
				encoded, _ := json.Marshal(item["changes"])
				diff = string(encoded)
			}
		}
	}
	if !strings.Contains(diff, `-beta\n+BETA`) || !strings.Contains(diff, "/w/app.txt") {
		t.Fatalf("file change diff: %s", diff)
	}

	// Picking a Codex model moves it back, with the Claude part in the briefing.
	h.Request("turn/start", map[string]any{"threadId": thread, "model": "gpt-x", "input": []any{map[string]any{"type": "text", "text": "back to codex"}}})
	_, seen = h.WaitFor(agentfake.IsMethod("turn/completed"))
	if ids := agentfake.ThreadIDs(seen); len(ids) != 1 || !ids[thread] {
		t.Fatalf("Codex notifications must use the UI thread ID: %v", ids)
	}
	turns := agentfake.ReadLog(t, "codex-turn")
	last := turns[len(turns)-1]
	// Codex resumes its own thread and hears only about the turns it missed.
	if !strings.HasPrefix(last, "codex-thread-1|") || !strings.Contains(last, "you are back") || !strings.Contains(last, "edited /w/app.txt") || strings.Contains(last, "User: hello") {
		t.Fatalf("codex briefing: %q", last)
	}

	// Returning to Claude Code resumes its session the same way.
	h.Request("turn/start", map[string]any{"threadId": thread, "model": "claude/sonnet", "input": []any{map[string]any{"type": "text", "text": "claude again"}}})
	h.WaitFor(agentfake.IsMethod("turn/completed"))
	prompts = agentfake.ReadLog(t, "claude-prompt")
	if back := prompts[len(prompts)-1]; !strings.Contains(back, "you are back") || !strings.Contains(back, "User: back to codex") || strings.Contains(back, "User: continue please") {
		t.Fatalf("returning Claude briefing: %q", back)
	}
	if args := agentfake.ReadLog(t, "claude-args"); !strings.Contains(args[len(args)-1], "--resume ") {
		t.Fatalf("returning Claude must resume its session: %q", args)
	}

	// The mixed conversation is listed once and resumes with its history.
	list := h.Response(h.Request("thread/list", map[string]any{"cwd": cwd}))
	encoded, _ = json.Marshal(list)
	if strings.Count(string(encoded), `"id":"`+thread+`"`) != 1 || !strings.Contains(string(encoded), "codex-other") {
		t.Fatalf("thread list: %s", encoded)
	}
	h2 := startRouterHarness(t, protocol.AgentCodex)
	h2.Response(h2.Request("initialize", map[string]any{}))
	resumed := h2.Response(h2.Request("thread/resume", map[string]any{"threadId": thread}))
	history, _ := json.Marshal(resumed["result"].(map[string]any)["thread"])
	for _, want := range []string{"hello", "continue please", "EDIT now", "back to codex", "claude again", "BETA"} {
		if !strings.Contains(string(history), want) {
			t.Fatalf("resumed history lacks %q: %s", want, history)
		}
	}
}

func TestAgentRouterClaudeDefaultInterruptAndGoals(t *testing.T) {
	agentfake.Setup(t)
	cwd := t.TempDir()
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": cwd}))
	result := started["result"].(map[string]any)
	thread := result["thread"].(map[string]any)["id"].(string)
	if result["model"] != protocol.ClaudeDefaultModel || len(thread) != 36 {
		t.Fatalf("claude thread: %v", result)
	}
	goal := h.Response(h.Request("thread/goal/get", map[string]any{"threadId": thread}))
	if goal["error"] != nil || goal["result"].(map[string]any)["goal"] != nil {
		t.Fatalf("a new Claude conversation has no goal: %v", goal)
	}
	h.Request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "SLOW job"}}})
	h.WaitFor(agentfake.IsMethod("item/agentMessage/delta"))
	h.Response(h.Request("turn/interrupt", map[string]any{"threadId": thread, "turnId": "x"}))
	done, _ := h.WaitFor(agentfake.IsMethod("turn/completed"))
	if status := done["params"].(map[string]any)["turn"].(map[string]any)["status"]; status != "interrupted" {
		t.Fatalf("interrupt status %v", status)
	}
	unsupported := h.Response(h.Request("thread/unsubscribe", map[string]any{"threadId": thread}))
	if failure, _ := unsupported["error"].(map[string]any); failure == nil || !strings.Contains(failure["message"].(string), "not supported in Claude Code conversations") {
		t.Fatalf("an unhandled request for a Claude conversation was not refused clearly: %v", unsupported)
	}
	h.Response(h.Request("thread/settings/update", map[string]any{"threadId": thread, "collaborationMode": map[string]any{"mode": "plan"}}))
	h.Request("turn/start", map[string]any{"threadId": thread, "model": "claude/opus", "effort": "high", "input": []any{map[string]any{"type": "text", "text": "plan it"}}})
	_, seen := h.WaitFor(agentfake.IsMethod("turn/completed"))
	if !strings.Contains(agentfake.AgentMessages(seen), "claude: plan it") {
		t.Fatalf("turn after restart: %v", seen)
	}
	args := agentfake.ReadLog(t, "claude-args")
	if len(args) != 2 || !strings.Contains(args[1], "--resume "+thread) || !strings.Contains(args[1], "--effort high") || !strings.Contains(args[1], "--model opus") || !strings.Contains(args[1], "--permission-mode plan") {
		t.Fatalf("effort change must restart on the same session: %q", args)
	}
	if controls := strings.Join(agentfake.ReadLog(t, "claude-control"), ","); !strings.Contains(controls, "interrupt") || !strings.Contains(controls, "set_permission_mode") {
		t.Fatalf("control requests: %s", controls)
	}
}

func TestAgentRouterClaudeGoals(t *testing.T) {
	agentfake.Setup(t)
	cwd := t.TempDir()
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": cwd}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	set := h.Response(h.Request("thread/goal/set", map[string]any{"threadId": thread, "objective": "ship it", "status": "active"}))
	if set["result"].(map[string]any)["goal"].(map[string]any)["status"] != "active" {
		t.Fatalf("goal set: %v", set)
	}
	// The router keeps working toward the goal until Claude reports completion.
	complete, seen := h.WaitFor(func(m map[string]any) bool {
		if m["method"] != "thread/goal/updated" {
			return false
		}
		return m["params"].(map[string]any)["goal"].(map[string]any)["status"] == "complete"
	})
	goal := complete["params"].(map[string]any)["goal"].(map[string]any)
	if goal["tokensUsed"].(float64) != 30 {
		t.Fatalf("goal token accounting: %v", goal)
	}
	turns, thinking := 0, false
	for _, m := range seen {
		if m["method"] == "turn/started" {
			turns++
		}
		if m["method"] == "jara/activity" && strings.Contains(m["params"].(map[string]any)["label"].(string), "~120 tokens") {
			thinking = true
		}
	}
	if turns != 2 || !thinking {
		t.Fatalf("expected two automatic turns with thinking progress, got %d turns, thinking %v", turns, thinking)
	}
	prompts := agentfake.ReadLog(t, "claude-prompt")
	if !strings.Contains(prompts[0], "[Active goal: ship it") {
		t.Fatalf("goal preamble missing: %q", prompts[0])
	}

	// A user message while a continuation runs waits for it instead of failing.
	h.Response(h.Request("thread/goal/set", map[string]any{"threadId": thread, "objective": "second goal", "status": "active"}))
	h.WaitFor(agentfake.IsMethod("turn/started"))
	id := h.Request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "user says hi"}}})
	reply := h.Response(id)
	if reply["error"] != nil {
		t.Fatalf("queued user turn failed: %v", reply)
	}
	h.WaitFor(func(m map[string]any) bool {
		return m["method"] == "item/agentMessage/delta" && strings.Contains(m["params"].(map[string]any)["delta"].(string), "user says hi")
	})

	// Switching to Codex carries the active goal.
	h.Response(h.Request("thread/goal/set", map[string]any{"threadId": thread, "status": "paused"}))
	h.Request("turn/start", map[string]any{"threadId": thread, "model": "gpt-x", "input": []any{map[string]any{"type": "text", "text": "codex now"}}})
	h.WaitFor(func(m map[string]any) bool {
		return m["method"] == "turn/completed" && m["params"].(map[string]any)["turn"].(map[string]any)["id"] == "ct"
	})
	if goals := agentfake.ReadLog(t, "codex-goal"); len(goals) != 1 || !strings.Contains(goals[0], "second goal|paused|marker=false") {
		t.Fatalf("goal carried to Codex: %q", goals)
	}
	if turns := agentfake.ReadLog(t, "codex-turn"); !strings.Contains(turns[len(turns)-1], "user says hi") {
		t.Fatalf("briefing must include the Claude part: %q", turns)
	}
}
