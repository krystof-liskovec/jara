package jara

import (
	"encoding/json"
	"testing"
)

// nextMatching reads a client's messages until one matches.
func (c *daemonTestClient) nextMatching(description string, ok func(map[string]any) bool) map[string]any {
	c.t.Helper()
	for i := 0; i < 64; i++ {
		if message := c.next(); ok(message) {
			return message
		}
	}
	c.t.Fatalf("client never received %s", description)
	return nil
}

func responseTo(id float64) func(map[string]any) bool {
	return func(message map[string]any) bool { return message["id"] == id }
}

// A phone attached through the host opens another conversation while the
// laptop's turn runs in this one. The session must still see that turn end,
// accept the laptop's queued prompt, and run it.
func TestSessionFollowsTheConversationAcrossViewers(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-a", ThreadLoaded: true, Initialized: true}})
	laptop, _ := h.attach()
	laptop.send(map[string]any{"id": 1, "method": "thread/resume", "params": map[string]any{"threadId": "th-a"}})
	laptop.nextMatching("its resume", responseTo(1))
	laptop.send(map[string]any{"id": 2, "method": "turn/start", "params": map[string]any{"threadId": "th-a"}})
	h.server.nextMethod("turn/start")
	h.waitState("laptop turn", func(s chatDaemonState) bool { return s.ActiveTurn && s.TurnThread == "th-a" })

	phone, _ := h.attach()
	phone.send(map[string]any{"id": 1, "method": "thread/resume", "params": map[string]any{"threadId": "th-old"}})
	phone.nextMatching("its resume", responseTo(1))
	h.waitState("phone's conversation", func(s chatDaemonState) bool { return s.Conversation == "th-old" })

	h.server.emit(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "th-a", "turn": map[string]any{"id": "turn-1", "status": "completed"}}})
	h.waitState("idle after the laptop's turn", func(s chatDaemonState) bool { return !s.ActiveTurn && s.TurnThread == "" })

	laptop.send(map[string]any{"id": 3, "method": "jara/queue/add", "params": map[string]any{"threadId": "th-a", "prompt": "next step"}})
	if response := laptop.nextMatching("the queue answer", responseTo(3)); response["error"] != nil {
		t.Fatalf("queuing in the laptop's conversation was refused: %v", response["error"])
	}
	started := h.server.nextMethod("turn/start")
	params, _ := started["params"].(map[string]any)
	if params["threadId"] != "th-a" || jsonString(t, params["input"]) != `[{"text":"next step","type":"text"}]` {
		t.Fatalf("queued prompt started as %v", started)
	}
	h.waitState("the queued turn", func(s chatDaemonState) bool { return s.ActiveTurn && s.TurnThread == "th-a" })
}

func TestTypingInAConversationMakesItTheSessions(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-old", ThreadLoaded: true, Initialized: true}})
	laptop, _ := h.attach()
	laptop.send(map[string]any{"id": 1, "method": "turn/start", "params": map[string]any{"threadId": "th-a"}})
	h.server.nextMethod("turn/start")
	state := h.waitState("the typed conversation", func(s chatDaemonState) bool { return s.Conversation == "th-a" })
	if !state.ActiveTurn || state.TurnThread != "th-a" {
		t.Fatalf("state = %+v", state)
	}
	// New windows open the conversation in use, not the stale one.
	if _, ready := h.attach(); ready.Conversation != "th-a" {
		t.Fatalf("a new window opens %q", ready.Conversation)
	}
	laptop.send(map[string]any{"id": 2, "method": "turn/start", "params": map[string]any{"threadId": "th-old"}})
	if response := laptop.nextMatching("the second turn's answer", responseTo(2)); response["error"] == nil {
		t.Fatal("a second turn started while one runs")
	}
}

func TestSubagentMessagesLeaveTheConversationAlone(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-a", ThreadLoaded: true, Initialized: true}})
	laptop, _ := h.attach()
	laptop.send(map[string]any{"id": 1, "method": "turn/start", "params": map[string]any{"threadId": "child-1", "jaraSubagent": true}})
	h.server.nextMethod("turn/start")
	laptop.nextMatching("its answer", responseTo(1))
	h.server.emit(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "child-1"}})
	laptop.send(map[string]any{"id": 2, "method": "ping/check"})
	laptop.nextMatching("the ping answer", responseTo(2))
	if state := h.state(); state.ActiveTurn || state.Conversation != "th-a" {
		t.Fatalf("a subagent message changed the session: %+v", state)
	}
}

func TestRequestsActOnAnyConversationOpenedInTheSession(t *testing.T) {
	state := chatDaemonState{Conversation: "th-old"}
	request := func(thread string) json.RawMessage {
		data, _ := json.Marshal(map[string]string{"threadId": thread})
		return data
	}
	if got := state.requestThread(request("th-a")); got != "th-old" {
		t.Fatalf("a conversation never opened here = %q", got)
	}
	state.noteOpened("th-a")
	if got := state.requestThread(request("th-a")); got != "th-a" {
		t.Fatalf("an opened conversation = %q", got)
	}
	if got := state.requestThread(nil); got != "th-old" {
		t.Fatalf("no conversation named = %q", got)
	}
}
