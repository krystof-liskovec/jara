package agentrouter

import (
	"strings"
	"testing"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentfake"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

func routedTurn(h *agentfake.Harness, thread string, input ...any) []map[string]any {
	h.Request("turn/start", map[string]any{"threadId": thread, "input": input})
	_, seen := h.WaitFor(agentfake.IsMethod("turn/completed"))
	return seen
}

func textInput(text string) map[string]any { return map[string]any{"type": "text", "text": text} }

func TestClaudeRewindCompactContextAndImages(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)

	seen := routedTurn(h, thread, textInput("first"))
	var usage map[string]any
	for _, m := range seen {
		if m["method"] == "thread/tokenUsage/updated" {
			usage = m["params"].(map[string]any)["tokenUsage"].(map[string]any)
		}
	}
	if usage == nil || usage["last"].(map[string]any)["totalTokens"] != float64(1010) || usage["modelContextWindow"] != float64(400000) {
		t.Fatalf("Claude context usage: %v", usage)
	}
	routedTurn(h, thread, textInput("second [Image #1]"), map[string]any{"type": "image", "url": "data:image/png;base64,iVBORw0KGgo="})
	if images := agentfake.ReadLog(t, "claude-image"); len(images) != 1 || images[0] != "image/png" {
		t.Fatalf("image block: %v", images)
	}

	if compact := h.Response(h.Request("thread/compact/start", map[string]any{"threadId": thread})); compact["error"] != nil {
		t.Fatalf("compact: %v", compact)
	}
	_, seen = h.WaitFor(agentfake.IsMethod("turn/completed"))
	compacted := false
	for _, m := range seen {
		if item, ok := m["params"].(map[string]any)["item"].(map[string]any); ok && m["method"] == "item/completed" && item["type"] == "contextCompaction" {
			compacted = true
		}
	}
	if prompts := agentfake.ReadLog(t, "claude-prompt"); !compacted || prompts[len(prompts)-1] != "/compact" {
		t.Fatalf("compaction: reported=%v prompts=%v", compacted, prompts)
	}

	if rewind := h.Response(h.Request("jara/rewind", map[string]any{"threadId": thread, "drop": 1})); rewind["error"] != nil {
		t.Fatalf("rewind: %v", rewind)
	}
	resumed := h.Response(h.Request("thread/resume", map[string]any{"threadId": thread}))
	turns := resumed["result"].(map[string]any)["thread"].(map[string]any)["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("history after rewind has %d turns: %v", len(turns), turns)
	}
	routedTurn(h, thread, textInput("third"))
	prompts := agentfake.ReadLog(t, "claude-prompt")
	briefing := prompts[len(prompts)-1]
	if !strings.Contains(briefing, "rewound this conversation") || !strings.Contains(briefing, "User: first") || strings.Contains(briefing, "second") || !strings.HasSuffix(briefing, "third") {
		t.Fatalf("rewind briefing: %q", briefing)
	}
	args := agentfake.ReadLog(t, "claude-args")
	if last := args[len(args)-1]; !strings.Contains(last, "--session-id") || strings.Contains(last, thread) {
		t.Fatalf("rewind must start a fresh Claude session: %q", args)
	}
}

func TestCodexRewindRevertsNatively(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	routedTurn(h, thread, textInput("one"))
	routedTurn(h, thread, textInput("two"))
	if rewind := h.Response(h.Request("jara/rewind", map[string]any{"threadId": thread, "drop": 1})); rewind["error"] != nil {
		t.Fatalf("rewind: %v", rewind)
	}
	if reverts := agentfake.ReadLog(t, "codex-revert"); len(reverts) != 1 || reverts[0] != thread+"|ct" {
		t.Fatalf("Codex revert: %v", reverts)
	}
	routedTurn(h, thread, textInput("three"))
	turns := agentfake.ReadLog(t, "codex-turn")
	if last := turns[len(turns)-1]; last != thread+"|three" {
		t.Fatalf("a natively reverted thread continues as is: %v", turns)
	}
}

func TestRewindNeedsATurn(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	if rewind := h.Response(h.Request("jara/rewind", map[string]any{"threadId": "unknown", "drop": 1})); rewind["error"] == nil {
		t.Fatalf("rewinding without a known turn must fail: %v", rewind)
	}
}

func TestRelativeChangePaths(t *testing.T) {
	if relativeToWorkspace("/w/s/a.go", "/w") != "s/a.go" || relativeToWorkspace("/x/a.go", "/w") != "/x/a.go" {
		t.Fatal("relative change paths")
	}
}
