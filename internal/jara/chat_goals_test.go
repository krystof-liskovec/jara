package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// goalTestConnection captures requests the UI writes to the app-server.
func goalTestUI(t *testing.T) (*chatUI, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var wire, out bytes.Buffer
	u := &chatUI{out: &out, pending: map[string]chatPending{}, items: map[string]json.RawMessage{}, thread: "t1", threadReady: true, persistentSession: true,
		c: &appConnection{encoder: json.NewEncoder(&wire), input: nopWriteCloser{&wire}}}
	return u, &wire, &out
}

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

func lastRequest(t *testing.T, wire *bytes.Buffer) (string, string, map[string]any) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(wire.String()), "\n")
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params map[string]any  `json:"params"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &request); err != nil {
		t.Fatal(err)
	}
	return string(request.ID), request.Method, request.Params
}

func TestGoalResponsesAreHandled(t *testing.T) {
	u, wire, out := goalTestUI(t)
	if err := u.runChatGoalCommand("/goal ship the parser"); err != nil {
		t.Fatal(err)
	}
	id, method, _ := lastRequest(t, wire)
	if method != "thread/goal/set" {
		t.Fatalf("request %s", method)
	}
	result, _ := json.Marshal(map[string]any{"goal": map[string]any{"threadId": "t1", "objective": "ship the parser", "status": "active"}})
	if err := u.response(appMessage{ID: json.RawMessage(id), Result: result}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Goal set. Status: active.") || u.goal == nil {
		t.Fatalf("goal set response not handled: %q", out.String())
	}
}

func TestGoalCrunchCreatesTheCrunchJob(t *testing.T) {
	u, wire, _ := goalTestUI(t)
	if err := u.runChatCrunchCommand("/goalcrunch finish it", true); err != nil {
		t.Fatal(err)
	}
	id, method, _ := lastRequest(t, wire)
	if method != "jara/crunch/status" {
		t.Fatalf("first request %s", method)
	}
	_ = u.response(appMessage{ID: json.RawMessage(id), Result: json.RawMessage(`{"job":null}`)})
	id, method, params := lastRequest(t, wire)
	if method != "thread/goal/set" || params["jaraDriver"] != "crunch" {
		t.Fatalf("goal request %s %v", method, params)
	}
	_ = u.response(appMessage{ID: json.RawMessage(id), Result: json.RawMessage(`{"goal":{"threadId":"t1","objective":"finish it","status":"active"}}`)})
	_, method, params = lastRequest(t, wire)
	if method != "jara/crunch/create" || params["mode"] != "goal" || params["prompt"] != "finish it" {
		t.Fatalf("goal crunch must create the crunch job, got %s %v", method, params)
	}
}
