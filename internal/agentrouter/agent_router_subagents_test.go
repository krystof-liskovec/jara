package agentrouter

import (
	"encoding/json"
	"strings"
	"testing"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentfake"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

func TestClaudeSubagentBecomesViewableThread(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)

	h.Request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "SUBAGENT please"}}})
	_, seen := h.WaitFor(agentfake.IsMethod("turn/completed"))
	if got := agentfake.ReadLog(t, "claude-forward-subagents"); len(got) != 1 || got[0] != "1" {
		t.Fatalf("Claude Code must forward subagent text: %q", got)
	}
	child := protocol.ClaudeSubagentThreadPrefix + "toolu_task"
	var spawned, activity, completed, updated bool
	for _, message := range seen {
		params, _ := message["params"].(map[string]any)
		item, _ := params["item"].(map[string]any)
		switch {
		case message["method"] == "item/started" && item["type"] == "collabAgentToolCall":
			receivers, _ := item["receiverThreadIds"].([]any)
			spawned = len(receivers) == 1 && receivers[0] == child && item["prompt"] == "List the test files"
		case item["type"] == "subAgentActivity" && item["kind"] == "started":
			activity = item["agentThreadId"] == child && item["agentPath"] == "Find tests"
		case message["method"] == "item/completed" && item["type"] == "collabAgentToolCall":
			states, _ := item["agentsStates"].(map[string]any)
			state, _ := states[child].(map[string]any)
			completed = state["status"] == "completed"
		case message["method"] == "jara/subagent/updated":
			updated = updated || params["threadId"] == child
		}
		if params["threadId"] == child && message["method"] != "jara/subagent/updated" {
			t.Fatalf("subagent events must not look like the conversation's: %v", message)
		}
	}
	if !spawned || !activity || !completed || !updated {
		t.Fatalf("spawned=%v activity=%v completed=%v updated=%v in %v", spawned, activity, completed, updated, seen)
	}

	read := h.Response(h.Request("thread/read", map[string]any{"threadId": child, "includeTurns": true}))
	encoded, _ := json.Marshal(read["result"])
	for _, want := range []string{`"jaraStatus":"completed"`, `"jaraAgentId":"a1b2"`, `List the test files`, `Looking for tests.`,
		`"command":"ls *_test.go"`, `"aggregatedOutput":"a_test.go"`, `Found a_test.go`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("subagent transcript lacks %s: %s", want, encoded)
		}
	}
	if strings.Count(string(encoded), "List the test files") != 1 {
		t.Fatalf("the Task prompt must appear once: %s", encoded)
	}

	// Claude Code cannot take a message straight to a subagent.
	refused := h.Response(h.Request("turn/start", map[string]any{"threadId": child, "jaraSubagent": true, "input": []any{map[string]any{"type": "text", "text": "hi"}}}))
	if refused["error"] == nil {
		t.Fatalf("direct message to a Claude subagent: %v", refused)
	}
}

func TestCodexSubagentMessagePassesThrough(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))

	h.Request("turn/start", map[string]any{"threadId": "codex-child-1", "jaraSubagent": true, "input": []any{map[string]any{"type": "text", "text": "check again"}}})
	h.WaitFor(agentfake.IsMethod("turn/completed"))
	if got := agentfake.ReadLog(t, "codex-turn"); len(got) != 1 || got[0] != "codex-child-1|check again" {
		t.Fatalf("subagent turn: %q", got)
	}
	if got := agentfake.ReadLog(t, "codex-subagent-marker"); len(got) != 0 {
		t.Fatalf("Jara's marker reached Codex: %q", got)
	}
	// The subagent must not become a conversation of its own.
	listed := h.Response(h.Request("thread/list", map[string]any{}))
	if encoded, _ := json.Marshal(listed); strings.Contains(string(encoded), "codex-child-1") {
		t.Fatalf("subagent listed as a conversation: %s", encoded)
	}
}
