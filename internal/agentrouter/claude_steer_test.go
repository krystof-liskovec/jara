package agentrouter

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/protocol"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestClaudeCodeTurnsTakeSteeredMessages(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	var ui, input bytes.Buffer
	thread := &Thread{ID: "th", Segments: []ThreadSegment{{Agent: protocol.AgentClaude, BackendID: "b1"}}}
	r := &agentRouter{out: &ui, threads: map[string]*Thread{"th": thread}}
	s := newClaudeSession(r, thread, "b1", false)
	s.cmd, s.stdin = &exec.Cmd{}, nopWriteCloser{&input}
	steer := func(id, turn, text string) string {
		ui.Reset()
		params, _ := json.Marshal(map[string]any{"threadId": "th", "expectedTurnId": turn, "input": []any{map[string]any{"type": "text", "text": text}}})
		s.steer(json.RawMessage(id), params)
		return ui.String()
	}

	thread.beginTurn("fix the tests", protocol.AgentClaude)
	s.openTurn(nil)
	running := s.turnID
	if reply := steer(`1`, "old-turn", "x"); !strings.Contains(reply, `"error"`) || input.Len() != 0 {
		t.Fatalf("a steer for another turn was taken: %s", reply)
	}
	if reply := steer(`2`, running, "and the docs"); !strings.Contains(reply, `"turnId":"`+running+`"`) || !strings.Contains(input.String(), `"text":"and the docs"`) {
		t.Fatalf("steer reply %s, input %s", reply, input.String())
	}
	// The next model call sees the message: the turn ends as usual.
	s.handleLine([]byte(`{"type":"stream_event","event":{"type":"message_start"}}`))
	s.finishTurn("completed", "")
	if s.turnID != "" || thread.Turns[0].User != "fix the tests\n\nand the docs" {
		t.Fatalf("after an answered steer: turn %q, transcript %q", s.turnID, thread.Turns[0].User)
	}

	// A message the ending turn did not reach runs as Claude Code's next
	// turn, which the router follows instead of dropping its reply.
	thread.beginTurn("second task", protocol.AgentClaude)
	s.openTurn(nil)
	steer(`3`, s.turnID, "one more thing")
	ui.Reset()
	s.finishTurn("completed", "")
	if s.turnID == "" || !strings.Contains(ui.String(), `"method":"turn/started"`) || thread.lastTurn().User != "one more thing" {
		t.Fatalf("no continuation turn: turn %q, ui %s", s.turnID, ui.String())
	}
}
