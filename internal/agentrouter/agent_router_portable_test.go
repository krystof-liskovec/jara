package agentrouter

import (
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/agentfake"
	"krystof.liskovec/jara/internal/portable"
	"krystof.liskovec/jara/internal/protocol"
)

// A moved Claude Code conversation whose native session is missing continues
// in a fresh session briefed on Jara's transcript instead of failing.
func TestPortableClaudeThreadRebriefsWithoutNativeSession(t *testing.T) {
	agentfake.Setup(t)
	t.Setenv(PortableThreadsEnv, "1")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	cwd := t.TempDir()
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": cwd}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)

	routedTurn(h, thread, textInput("first"))
	if record := LoadThread(thread); record == nil || !record.Portable {
		t.Fatalf("portable conversation was not recorded: %+v", record)
	}
	// The native session exists: the next turn resumes it.
	if err := portable.WriteFileAtomic(portable.ClaudeSessionPath(cwd, thread), []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	routedTurn(h, thread, textInput("second"))
	if prompts := agentfake.ReadLog(t, "claude-prompt"); prompts[len(prompts)-1] != "second" {
		t.Fatalf("resumed turn prompt: %q", prompts[len(prompts)-1])
	}
	// Moved to a machine without it: the next turn is briefed instead.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	routedTurn(h, thread, textInput("third"))
	prompts := agentfake.ReadLog(t, "claude-prompt")
	briefing := prompts[len(prompts)-1]
	if !strings.Contains(briefing, "moved to another machine") || !strings.Contains(briefing, "User: first") || !strings.HasSuffix(briefing, "third") {
		t.Fatalf("moved briefing: %q", briefing)
	}
}

// A moved Codex conversation whose rollout was not restored starts its next
// turn in a fresh Codex thread briefed on the transcript.
func TestMovedCodexThreadRebriefs(t *testing.T) {
	agentfake.Setup(t)
	t.Setenv("JARA_FAKE_CODEX_RESUME_ERROR", "no rollout found for thread id codex-moved")
	record := &Thread{ID: "codex-moved", CWD: t.TempDir(), Turns: []TranscriptTurn{{User: "earlier", Agent: protocol.AgentCodex, Reply: "done"}},
		Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: "codex-moved", StartTurn: 1}}}
	record.MarkMoved()
	if err := StoreThread(record); err != nil {
		t.Fatal(err)
	}
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	if resumed := h.Response(h.Request("thread/resume", map[string]any{"threadId": "codex-moved"})); resumed["error"] != nil {
		t.Fatalf("resume: %v", resumed)
	}
	seen := routedTurn(h, "codex-moved", textInput("next"))
	for _, m := range seen {
		if method, _ := m["method"].(string); method == "turn/started" || method == "turn/completed" {
			if thread := m["params"].(map[string]any)["threadId"]; thread != "codex-moved" {
				t.Fatalf("%s reports thread %v, not the conversation", method, thread)
			}
		}
	}
	turns := agentfake.ReadLog(t, "codex-turn")
	if last := turns[len(turns)-1]; strings.HasPrefix(last, "codex-moved|") || !strings.Contains(last, "moved to another machine") || !strings.Contains(last, "User: earlier") {
		t.Fatalf("moved Codex turn: %q", turns)
	}
}

func TestEmptyPortableCodexThreadResumesWithoutRollout(t *testing.T) {
	agentfake.Setup(t)
	const threadID = "codex-empty"
	t.Setenv("JARA_FAKE_CODEX_RESUME_ERROR", "no rollout found for thread id "+threadID)
	record := &Thread{ID: threadID, CWD: t.TempDir(),
		Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: threadID, StartTurn: 1}}}
	if err := StoreThread(record); err != nil {
		t.Fatal(err)
	}
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	resumed := h.Response(h.Request("thread/resume", map[string]any{"threadId": threadID}))
	if resumed["error"] != nil {
		t.Fatalf("resume: %v", resumed)
	}
	thread := resumed["result"].(map[string]any)["thread"].(map[string]any)
	if thread["id"] != threadID {
		t.Fatalf("resumed with a different conversation ID: %v", thread)
	}
	seen := routedTurn(h, threadID, textInput("first prompt"))
	if ids := agentfake.ThreadIDs(seen); len(ids) != 1 || !ids[threadID] {
		t.Fatalf("turn notifications must retain the conversation ID: %v", ids)
	}
	turns := agentfake.ReadLog(t, "codex-turn")
	if len(turns) != 1 || strings.HasPrefix(turns[0], threadID+"|") || !strings.HasSuffix(turns[0], "|first prompt") {
		t.Fatalf("first prompt should reach a new Codex session verbatim: %q", turns)
	}
	if got := LoadThread(threadID); got == nil || got.Rebrief || got.Current().BackendID == threadID || len(got.Turns) != 1 {
		t.Fatalf("recovered conversation was not saved: %+v", got)
	}
}

func TestEmptyPortableCodexThreadPropagatesOtherResumeErrors(t *testing.T) {
	agentfake.Setup(t)
	const threadID = "codex-unavailable"
	const failure = "conversation is already active"
	t.Setenv("JARA_FAKE_CODEX_RESUME_ERROR", failure)
	if err := StoreThread(&Thread{ID: threadID, CWD: t.TempDir(),
		Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: threadID, StartTurn: 1}}}); err != nil {
		t.Fatal(err)
	}
	h := startRouterHarness(t, protocol.AgentCodex)
	h.Response(h.Request("initialize", map[string]any{}))
	resumed := h.Response(h.Request("thread/resume", map[string]any{"threadId": threadID}))
	if rpcError, _ := resumed["error"].(map[string]any); rpcError == nil || rpcError["message"] != failure {
		t.Fatalf("resume should report the original error: %v", resumed)
	}
}
