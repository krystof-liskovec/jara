package agentrouter

import (
	"encoding/json"
	"testing"

	"krystof.liskovec/jara/internal/protocol"
)

func TestTranscriptRemembersTheModelBehindEachReply(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	thread := &Thread{ID: "th", Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: "backend", StartTurn: 1}}}
	r := &agentRouter{threads: map[string]*Thread{"th": thread}, codexToVirtual: map[string]string{"backend": "th"}}

	// Codex reports the model a resumed thread runs with; its reply keeps it.
	r.noteCodexSettings("thread/resume", json.RawMessage(`{"thread":{"id":"backend"},"model":"gpt-5.5","reasoningEffort":"high"}`), nil)
	thread.beginTurn("hi", protocol.AgentCodex)
	params, _ := json.Marshal(map[string]any{"threadId": "th", "item": map[string]any{"type": "agentMessage", "text": "Hello."}})
	r.observeCodexNotification(rpcMessage{Method: "item/completed", Params: params})
	// Claude Code turns record the model they ran; older turns recorded none.
	thread.Turns = append(thread.Turns,
		TranscriptTurn{User: "more", Agent: protocol.AgentClaude, Reply: "Sure.", Model: "claude/opus", Effort: "xhigh", Resolved: "claude-opus-5-5"},
		TranscriptTurn{User: "old", Agent: protocol.AgentClaude, Reply: "From before."})

	reply := func(turn map[string]any) map[string]any {
		for _, item := range turn["items"].([]map[string]any) {
			if item["type"] == "agentMessage" {
				return item
			}
		}
		t.Fatalf("no reply in %v", turn)
		return nil
	}
	turns := thread.HistoryTurns()
	if codex := reply(turns[0]); codex["jaraModel"] != "gpt-5.5" || codex["jaraEffort"] != "high" {
		t.Fatalf("Codex reply %v", codex)
	}
	if claude := reply(turns[1]); claude["jaraModel"] != "claude/opus" || claude["jaraEffort"] != "xhigh" || claude["jaraResolvedModel"] != "claude-opus-5-5" {
		t.Fatalf("Claude Code reply %v", claude)
	}
	if old := reply(turns[2]); old["jaraModel"] != nil || old["jaraAgent"] != protocol.AgentClaude {
		t.Fatalf("older reply %v", old)
	}

	// A resume reporting the default effort clears the previous one.
	r.noteCodexSettings("thread/start", json.RawMessage(`{"thread":{"id":"th"},"model":"gpt-6","reasoningEffort":null}`), nil)
	if thread.CodexModel != "gpt-6" || thread.CodexEffort != "" {
		t.Fatalf("settings %q %q", thread.CodexModel, thread.CodexEffort)
	}
}
