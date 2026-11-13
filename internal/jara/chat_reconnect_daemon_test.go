package jara

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// markReconnectingClients makes the first n attached clients count as relays
// for clients that reconnect by themselves.
func markReconnectingClients(t *testing.T, n int32) {
	t.Helper()
	original := chatClientReconnects
	var seen atomic.Int32
	chatClientReconnects = func(net.Conn) bool { return seen.Add(1) <= n }
	t.Cleanup(func() { chatClientReconnects = original })
}

func expectSessionEnded(t *testing.T, client *daemonTestClient, reconnect bool) {
	t.Helper()
	for {
		message := client.next()
		if message["method"] != "jara/session/ended" {
			continue
		}
		params, _ := message["params"].(map[string]any)
		if params["reconnect"] != reconnect {
			t.Fatalf("session ended with %v, want reconnect=%v", params, reconnect)
		}
		client.expectClosed()
		return
	}
}

func TestSuspendStopsSessionWithReconnectingClients(t *testing.T) {
	markReconnectingClients(t, 1)
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true}})
	client, _ := h.attach()
	h.waitState("reconnecting client", func(s chatDaemonState) bool { return s.ReconnectingClients == 1 })
	if err := sendChatDaemonControl(h.paths.socket, "jara/suspend"); err != nil {
		t.Fatal(err)
	}
	expectSessionEnded(t, client, true)
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSuspendKeepsSessionWithOtherClients(t *testing.T) {
	markReconnectingClients(t, 0)
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true}})
	client, _ := h.attach()
	h.waitState("attached", func(s chatDaemonState) bool { return s.AttachedClients == 1 })
	if err := sendChatDaemonControl(h.paths.socket, "jara/suspend"); err != nil {
		t.Fatal(err)
	}
	client.expectNone(200 * time.Millisecond)
	h.assertRunning()
	client.send(map[string]string{"method": "jara/close"})
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestStopTellsOtherClientsNotToReconnect(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true}})
	phone, _ := h.attach()
	computer, _ := h.attach()
	h.waitState("two clients", func(s chatDaemonState) bool { return s.AttachedClients == 2 })
	phone.send(map[string]string{"method": "jara/stop"})
	expectSessionEnded(t, computer, false)
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestReconnectingClientsDoNotBlockUpdates(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	lock, _ := writeIdleDaemonLock(t, "w-rc", chatDaemonState{AttachedClients: 2, ReconnectingClients: 2, Attached: true})
	if _, idle := idleChatDaemonWorkspace(lock, false); idle {
		t.Fatal("sync treated a session with attached clients as idle")
	}
	if _, idle := idleChatDaemonWorkspace(lock, true); !idle {
		t.Fatal("update blocked by clients that reconnect by themselves")
	}
	lock, _ = writeIdleDaemonLock(t, "w-mixed", chatDaemonState{AttachedClients: 2, ReconnectingClients: 1, Attached: true})
	if _, idle := idleChatDaemonWorkspace(lock, true); idle {
		t.Fatal("update ignored a client that does not reconnect")
	}
}
