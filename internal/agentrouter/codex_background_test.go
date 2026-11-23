package agentrouter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func newCodexBackgroundRouter(t *testing.T) (*agentRouter, *bytes.Buffer, chan routerEvent) {
	t.Helper()
	t.Setenv("JARA_DATA", t.TempDir())
	var codex, ui bytes.Buffer
	events := make(chan routerEvent, 8)
	thread := &Thread{ID: "main", Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: "main"}}}
	r := &agentRouter{out: &ui, events: events, codexIn: json.NewEncoder(&codex), codexCalls: map[string]codexCall{},
		threads: map[string]*Thread{"main": thread}}
	return r, &codex, events
}

func codexNotify(t *testing.T, r *agentRouter, method string, params any) {
	t.Helper()
	data, _ := json.Marshal(params)
	if err := r.fromCodex(rpcMessage{Method: method, Params: data}); err != nil {
		t.Fatal(err)
	}
}

func spawnCodexChild(t *testing.T, r *agentRouter) {
	codexNotify(t, r, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t1"}})
	codexNotify(t, r, "item/completed", map[string]any{"threadId": "main", "item": map[string]any{
		"id": "a", "type": "subAgentActivity", "kind": "started", "agentThreadId": "child", "agentPath": "tests"}})
	codexNotify(t, r, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t1", "status": "completed"}})
}

func waitEvent(t *testing.T, events chan routerEvent, source string) routerEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.source != source {
			t.Fatalf("event %+v, want %s", event, source)
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatalf("no %s event", source)
	}
	return routerEvent{}
}

func TestIdleCodexMainAgentIsToldWhenASubagentFinishes(t *testing.T) {
	r, codex, events := newCodexBackgroundRouter(t)
	defer func(wake, check time.Duration) { SubagentWakeDelay, SubagentCheckInterval = wake, check }(SubagentWakeDelay, SubagentCheckInterval)
	SubagentWakeDelay, SubagentCheckInterval = time.Millisecond, time.Hour
	spawnCodexChild(t, r)
	codexNotify(t, r, "thread/status/changed", map[string]any{"threadId": "child", "status": map[string]any{"type": "idle"}})
	event := waitEvent(t, events, "codex-wake")
	codex.Reset()
	r.wakeCodexParent(event.thread)
	if !strings.Contains(codex.String(), `"method":"turn/start"`) || !strings.Contains(codex.String(), `Your subagent \"tests\" finished`) {
		t.Fatalf("not woken: %s", codex.String())
	}
	if r.threads["main"].lastTurn().User != `(subagent "tests" finished)` {
		t.Fatalf("label %q", r.threads["main"].lastTurn().User)
	}
	// Unloading the finished subagent later wakes nobody.
	codexNotify(t, r, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t2", "status": "completed"}})
	codexNotify(t, r, "thread/status/changed", map[string]any{"threadId": "child", "status": map[string]any{"type": "notLoaded"}})
	if len(r.background().finished["main"]) != 0 {
		t.Fatal("an already finished subagent woke the main agent again")
	}
}

func TestCodexContinuingByItselfOrAUserMessageNeedsNoWake(t *testing.T) {
	r, codex, events := newCodexBackgroundRouter(t)
	defer func(wake, check time.Duration) { SubagentWakeDelay, SubagentCheckInterval = wake, check }(SubagentWakeDelay, SubagentCheckInterval)
	SubagentWakeDelay, SubagentCheckInterval = time.Millisecond, time.Hour
	spawnCodexChild(t, r)
	codexNotify(t, r, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"status": "completed"}})
	event := waitEvent(t, events, "codex-wake")
	codexNotify(t, r, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t2"}})
	codex.Reset()
	r.wakeCodexParent(event.thread)
	if codex.Len() != 0 {
		t.Fatalf("woke a main agent that already continued: %s", codex.String())
	}
	codexNotify(t, r, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t2", "status": "completed"}})
	// You message the subagent directly: its answer is for you.
	r.noteUserSubagentTurn("child")
	codexNotify(t, r, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"status": "completed"}})
	if len(r.background().finished["main"]) != 0 {
		t.Fatal("a reply to you woke the main agent")
	}
}

func TestSilentCodexSubagentsAreChecked(t *testing.T) {
	r, codex, events := newCodexBackgroundRouter(t)
	defer func(wake, check time.Duration) { SubagentWakeDelay, SubagentCheckInterval = wake, check }(SubagentWakeDelay, SubagentCheckInterval)
	SubagentWakeDelay, SubagentCheckInterval = time.Hour, time.Millisecond
	spawnCodexChild(t, r)
	event := waitEvent(t, events, "codex-check")
	codex.Reset()
	r.checkCodexChildren(event.thread)
	var call rpcMessage
	if err := json.Unmarshal(codex.Bytes(), &call); err != nil || call.Method != "thread/read" || !strings.Contains(string(call.Params), `"child"`) {
		t.Fatalf("check: %v %s", err, codex.String())
	}
	result, _ := json.Marshal(map[string]any{"thread": map[string]any{"status": map[string]any{"type": "idle"}}})
	if err := r.fromCodex(rpcMessage{ID: call.ID, Result: result}); err != nil {
		t.Fatal(err)
	}
	if names := r.background().finished["main"]; len(names) != 1 || names[0] != "tests" {
		t.Fatalf("finished %v", names)
	}
}
