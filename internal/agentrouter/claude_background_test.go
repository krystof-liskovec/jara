package agentrouter

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func newBackgroundTestSession(t *testing.T) (*claudeSession, *Thread, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("JARA_DATA", t.TempDir())
	var ui, input bytes.Buffer
	thread := &Thread{ID: "th", Segments: []ThreadSegment{{Agent: protocol.AgentClaude, BackendID: "b1"}}}
	r := &agentRouter{out: &ui, threads: map[string]*Thread{"th": thread}}
	s := newClaudeSession(r, thread, "b1", false)
	s.cmd, s.stdin = &exec.Cmd{}, nopWriteCloser{&input}
	s.model, s.effort, s.credentials = protocol.ClaudeCLIModel(thread.Model), thread.Effort, claudeCredentialStamp()
	return s, thread, &ui, &input
}

// A background subagent outlives the turn that started it.
func startBackgroundSubagent(s *claudeSession, thread *Thread) {
	thread.beginTurn("research this", protocol.AgentClaude)
	s.openTurn(nil)
	s.handleLine([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Agent","input":{"description":"survey","prompt":"look around","run_in_background":true}}]}}`))
	s.handleLine([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"Started in the background. agentId: a1"}]}}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success"}`))
}

func TestClaudeCodeContinuingAfterABackgroundSubagentIsFollowed(t *testing.T) {
	s, thread, ui, _ := newBackgroundTestSession(t)
	startBackgroundSubagent(s, thread)
	if s.turnID != "" || s.r.claudeAgents[protocol.ClaudeSubagentThreadPrefix+"tu1"].status != "running" {
		t.Fatalf("turn %q, subagent %+v", s.turnID, s.r.claudeAgents)
	}
	s.handleLine([]byte(`{"type":"system","subtype":"task_notification","tool_use_id":"tu1","status":"completed","summary":"found it"}`))
	if s.r.claudeAgents[protocol.ClaudeSubagentThreadPrefix+"tu1"].status != "completed" {
		t.Fatal("the background subagent is still running")
	}
	// Claude Code takes the result up by itself: its reply is a turn.
	ui.Reset()
	s.handleLine([]byte(`{"type":"stream_event","event":{"type":"message_start"}}`))
	if s.turnID == "" || !strings.Contains(ui.String(), `"method":"turn/started"`) || thread.lastTurn().User != `(background agent "survey" finished)` {
		t.Fatalf("not followed: turn %q, ui %s, user %q", s.turnID, ui.String(), thread.lastTurn().User)
	}
	s.handleLine([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"It found it."}}}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success"}`))
	if s.turnID != "" || thread.lastTurn().Reply != "It found it." {
		t.Fatalf("turn %q, reply %q", s.turnID, thread.lastTurn().Reply)
	}
	// The fallback finds the conversation already continued.
	turns := len(thread.Turns)
	s.wakeIfIdle()
	if s.turnID != "" || len(thread.Turns) != turns {
		t.Fatal("woke the main agent a second time")
	}
}

func TestIdleMainAgentIsToldWhenABackgroundSubagentFinishes(t *testing.T) {
	s, thread, ui, input := newBackgroundTestSession(t)
	startBackgroundSubagent(s, thread)
	s.handleLine([]byte(`{"type":"system","subtype":"task_notification","tool_use_id":"tu1","status":"failed"}`))
	input.Reset()
	ui.Reset()
	s.wakeIfIdle()
	if s.turnID == "" || !strings.Contains(ui.String(), `"method":"turn/started"`) || !strings.Contains(input.String(), `background agent \"survey\" failed`) {
		t.Fatalf("not woken: turn %q, ui %s, input %s", s.turnID, ui.String(), input.String())
	}
	if thread.lastTurn().User != `(background agent "survey" failed)` {
		t.Fatalf("label %q", thread.lastTurn().User)
	}
}

func TestBackgroundWakeWaitsForTheRouterLoop(t *testing.T) {
	s, thread, _, _ := newBackgroundTestSession(t)
	events := make(chan routerEvent, 1)
	s.r.events = events
	defer func(delay time.Duration) { SubagentWakeDelay = delay }(SubagentWakeDelay)
	SubagentWakeDelay = time.Millisecond
	startBackgroundSubagent(s, thread)
	s.handleLine([]byte(`{"type":"system","subtype":"task_notification","tool_use_id":"tu1","status":"completed"}`))
	select {
	case event := <-events:
		if event.source != "claude-wake" || event.session != s || event.process != s.cmd {
			t.Fatalf("event %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no wake event")
	}
	// While the main agent works, Claude Code delivers the notification itself.
	thread.beginTurn("next", protocol.AgentClaude)
	s.openTurn(nil)
	s.handleLine([]byte(`{"type":"system","subtype":"task_notification","tool_use_id":"tu1","status":"completed"}`))
	if s.wakeText != "" {
		t.Fatalf("a running turn was to be woken: %q", s.wakeText)
	}
}
