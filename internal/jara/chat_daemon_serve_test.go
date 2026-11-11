package jara

import (
	"strings"
	"testing"
	"time"
)

func TestChatDaemonAttachReceivesStateAndCloseStopsDaemon(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Workspace: "demo", Conversation: "th-0", Initialized: true, ThreadLoaded: true}})
	client, ready := h.attach()
	if ready.Workspace != "demo" || ready.Conversation != "th-0" || !ready.Initialized || ready.ActiveTurn || ready.CWD == "" {
		t.Fatalf("ready = %+v", ready)
	}
	state := h.waitState("attached", func(s chatDaemonState) bool { return s.AttachedClients == 1 })
	if !state.Attached || state.WorkspaceID != "w-test" {
		t.Fatalf("attached state = %+v", state)
	}

	client.send(map[string]string{"method": "jara/close"})
	if err := h.wait(); err != nil {
		t.Fatalf("close returned %v", err)
	}
	client.expectClosed()
	<-h.server.closed // the app-server input was closed during shutdown
	if final := h.state(); final.Attached || final.AttachedClients != 0 || final.ActiveTurn || final.Conversation != "th-0" {
		t.Fatalf("final state = %+v", final)
	}
}

func TestChatDaemonRoutesResponsesOnlyToRequestingClient(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{})
	first, _ := h.attach()
	second, _ := h.attach()
	h.waitState("two clients", func(s chatDaemonState) bool { return s.AttachedClients == 2 })

	// Both clients use the same JSON-RPC ID; the daemon must keep them apart.
	first.send(map[string]any{"id": 1, "method": "thread/start", "params": map[string]any{}})
	forwarded := h.server.nextMethod("thread/start")
	if id, _ := forwarded["id"].(string); !strings.HasPrefix(id, "jara-client-") {
		t.Fatalf("forwarded request kept client ID: %v", forwarded)
	}
	response := first.next()
	if response["id"] != float64(1) || jsonString(t, response["result"]) != `{"thread":{"id":"th-1"}}` {
		t.Fatalf("first client response = %v", response)
	}

	second.send(map[string]any{"id": 1, "method": "model/list"})
	secondForwarded := h.server.nextMethod("model/list")
	if secondForwarded["id"] == forwarded["id"] {
		t.Fatalf("virtual IDs collide: %v", secondForwarded["id"])
	}
	if response = second.next(); response["id"] != float64(1) || jsonString(t, response["result"]) != `{"echo":"model/list"}` {
		t.Fatalf("second client response = %v", response)
	}

	h.server.emit(map[string]any{"method": "thread/status", "params": map[string]any{"ok": true}})
	for _, client := range []*daemonTestClient{first, second} {
		if message := client.next(); message["method"] != "thread/status" {
			t.Fatalf("expected broadcast notification, got %v", message)
		}
	}
	state := h.waitState("thread loaded", func(s chatDaemonState) bool { return s.ThreadLoaded })
	if state.Conversation != "th-1" {
		t.Fatalf("conversation = %q", state.Conversation)
	}

	// Responses for unknown IDs are broadcast rather than dropped.
	h.server.emit(map[string]any{"id": "orphan", "result": map[string]any{}})
	for _, client := range []*daemonTestClient{first, second} {
		if message := client.next(); message["id"] != "orphan" {
			t.Fatalf("expected orphan response broadcast, got %v", message)
		}
	}
}

func TestChatDaemonReplaysApprovalAfterReattach(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true}})
	client, _ := h.attach()
	client.send(map[string]any{"id": 7, "method": "turn/start", "params": map[string]any{"threadId": "th-0"}})
	h.server.nextMethod("turn/start")
	if response := client.next(); response["id"] != float64(7) {
		t.Fatalf("turn response = %v", response)
	}
	if started := client.next(); started["method"] != "turn/started" {
		t.Fatalf("turn notification = %v", started)
	}
	approval := map[string]any{"id": "ap-1", "method": "item/commandExecution/requestApproval", "params": map[string]any{"command": "ls"}}
	h.server.emit(approval)
	if message := client.next(); message["id"] != "ap-1" {
		t.Fatalf("approval was not delivered: %v", message)
	}

	client.send(map[string]string{"method": "jara/detach"})
	client.expectClosed()
	state := h.waitState("detached", func(s chatDaemonState) bool { return !s.Attached })
	if !state.ActiveTurn {
		t.Fatalf("detach lost the running turn: %+v", state)
	}
	h.assertRunning()

	again, ready := h.attach()
	if !ready.ActiveTurn {
		t.Fatalf("reattach ready = %+v", ready)
	}
	if replay := again.next(); jsonString(t, replay) != jsonString(t, approval) {
		t.Fatalf("approval replay = %v", replay)
	}
	again.send(map[string]any{"id": "ap-1", "result": map[string]string{"decision": "accept"}})
	answer := h.server.next()
	if answer["id"] != "ap-1" || jsonString(t, answer["result"]) != `{"decision":"accept"}` {
		t.Fatalf("approval answer = %v", answer)
	}
	// A second answer for the same request is not forwarded.
	again.send(map[string]any{"id": "ap-1", "result": map[string]string{"decision": "decline"}})
	again.send(map[string]any{"id": 9, "method": "ping/check"})
	if next := h.server.next(); next["method"] != "ping/check" {
		t.Fatalf("duplicate approval answer was forwarded: %v", next)
	}
	again.next() // ping response

	h.server.emit(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "completed"}}})
	if message := again.next(); message["method"] != "turn/completed" {
		t.Fatalf("turn completion = %v", message)
	}
	h.waitState("idle", func(s chatDaemonState) bool { return !s.ActiveTurn })
	third, _ := h.attach()
	third.expectNone(100 * time.Millisecond)
	third.send(map[string]string{"method": "jara/stop"})
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestChatDaemonRejectsConcurrentTurns(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true}})
	client, _ := h.attach()
	client.send(map[string]any{"id": 1, "method": "turn/start", "params": map[string]any{"threadId": "th-0"}})
	h.server.nextMethod("turn/start")
	client.next()
	client.next()
	h.waitState("active", func(s chatDaemonState) bool { return s.ActiveTurn })

	client.send(map[string]any{"id": 2, "method": "turn/start", "params": map[string]any{"threadId": "th-0"}})
	rejected := client.next()
	if rejected["id"] != float64(2) || !strings.Contains(jsonString(t, rejected["error"]), "another turn is already running") {
		t.Fatalf("second turn was not rejected: %v", rejected)
	}
	client.send(map[string]any{"id": 3, "method": "ping/check"})
	if next := h.server.next(); next["method"] != "ping/check" {
		t.Fatalf("rejected turn reached the app-server: %v", next)
	}
	client.next()

	h.server.emit(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "completed"}}})
	client.next()
	h.waitState("idle", func(s chatDaemonState) bool { return !s.ActiveTurn })

	// A turn/start rejected by the app-server clears the active flag again.
	h.server.setHandler(nil)
	client.send(map[string]any{"id": 4, "method": "turn/start", "params": map[string]any{"threadId": "th-0"}})
	forwarded := h.server.nextMethod("turn/start")
	h.waitState("optimistically active", func(s chatDaemonState) bool { return s.ActiveTurn })
	h.server.emit(map[string]any{"id": forwarded["id"], "error": map[string]any{"message": "rejected"}})
	if response := client.next(); response["id"] != float64(4) || response["error"] == nil {
		t.Fatalf("rejected turn response = %v", response)
	}
	h.waitState("idle after rejection", func(s chatDaemonState) bool { return !s.ActiveTurn })
}

func TestChatDaemonTracksInitializationAndThreadState(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true}})
	h.server.setHandler(nil)
	client, ready := h.attach()
	if ready.Initialized {
		t.Fatal("daemon claims initialization before the handshake")
	}
	client.send(map[string]any{"id": 1, "method": "initialize"})
	initialize := h.server.nextMethod("initialize")
	h.server.emit(map[string]any{"id": initialize["id"], "error": map[string]any{"message": "Already initialized"}})
	client.next()
	h.waitState("initialized after duplicate handshake", func(s chatDaemonState) bool { return s.Initialized })

	client.send(map[string]any{"method": "initialized"})
	if forwarded := h.server.next(); forwarded["method"] != "initialized" {
		t.Fatalf("initialized notification not forwarded: %v", forwarded)
	}

	client.send(map[string]any{"id": 2, "method": "thread/resume", "params": map[string]any{"threadId": "missing"}})
	resume := h.server.nextMethod("thread/resume")
	h.waitState("thread unloaded during resume", func(s chatDaemonState) bool { return !s.ThreadLoaded })
	h.server.emit(map[string]any{"id": resume["id"], "error": map[string]any{"message": "not found"}})
	client.next()
	state := h.waitState("previous thread restored", func(s chatDaemonState) bool { return s.ThreadLoaded })
	if state.Conversation != "th-0" {
		t.Fatalf("failed resume replaced conversation: %+v", state)
	}

	client.send(map[string]any{"id": 3, "method": "thread/resume", "params": map[string]any{"threadId": "th-9"}})
	resume = h.server.nextMethod("thread/resume")
	h.server.emit(map[string]any{"id": resume["id"], "result": map[string]any{"thread": map[string]any{"id": "th-9"}}})
	client.next()
	h.waitState("resumed thread", func(s chatDaemonState) bool { return s.Conversation == "th-9" && s.ThreadLoaded })
}
