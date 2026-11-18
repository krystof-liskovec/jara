//go:build linux || darwin

package jara

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPromptsDuringATurnSteerIt(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	u := term.u
	u.setEffectiveModel("gpt-5.5")
	u.busy, u.turn = true, "turn-1"

	typeChatKeys(t, term, "also update the docs\r")
	requests := rawChatRequests(wire)
	if len(requests) != 1 || requests[0].Method != "turn/steer" {
		t.Fatalf("requests %+v", requests)
	}
	var params struct {
		ThreadID       string `json:"threadId"`
		ExpectedTurnID string `json:"expectedTurnId"`
		Input          []struct{ Text string }
	}
	_ = json.Unmarshal(requests[0].Params, &params)
	if params.ThreadID != u.thread || params.ExpectedTurnID != "turn-1" || len(params.Input) != 1 || params.Input[0].Text != "also update the docs" {
		t.Fatalf("steer params %s", requests[0].Params)
	}
	if term.editor.Text() != "" || !strings.Contains(chatScreenText(term.frame(100, 30, time.Now())), "also update the docs") {
		t.Fatal("the steered prompt shows in the conversation and leaves the input box")
	}

	// A turn that refuses the message, such as one just ending, queues it.
	var sent struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(lastWireLine(wire), &sent)
	var refusal appMessage
	_ = json.Unmarshal([]byte(`{"id":`+string(sent.ID)+`,"error":{"message":"no active turn"}}`), &refusal)
	if err := u.response(refusal); err != nil {
		t.Fatal(err)
	}
	if len(u.queuedPrompts) != 1 || u.queuedPrompts[0].text != "also update the docs" {
		t.Fatalf("queue %+v", u.queuedPrompts)
	}

	// Claude Code turns are steered too; the router delivers the message.
	wire.Reset()
	u.queuedPrompts = nil
	u.setEffectiveModel("claude/opus")
	typeChatKeys(t, term, "and the changelog\r")
	if requests = rawChatRequests(wire); len(requests) != 1 || requests[0].Method != "turn/steer" || len(u.queuedPrompts) != 0 {
		t.Fatalf("Claude Code: requests %+v, queue %+v", requests, u.queuedPrompts)
	}
	// Before the turn has started there is nothing to steer: it queues.
	wire.Reset()
	u.turn = ""
	typeChatKeys(t, term, "and the readme\r")
	if requests = rawChatRequests(wire); len(requests) != 0 || len(u.queuedPrompts) != 1 {
		t.Fatalf("no turn yet: requests %+v, queue %+v", requests, u.queuedPrompts)
	}
}

func lastWireLine(wire interface{ String() string }) []byte {
	lines := strings.Split(strings.TrimSpace(wire.String()), "\n")
	return []byte(lines[len(lines)-1])
}
