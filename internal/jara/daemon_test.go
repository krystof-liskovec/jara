package jara

import (
	"bufio"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanLogindSignalsTellsSuspendFromShutdown(t *testing.T) {
	cases := map[string]struct {
		input    string
		event    bool
		shutdown bool
	}{
		"suspend": {input: "signal time=1 sender=:1.2 -> destination=(null) path=/org/freedesktop/login1; interface=org.freedesktop.login1.Manager; member=PrepareForSleep\n   boolean true\n",
			event: true},
		"shutdown": {input: "signal member=PrepareForShutdown\n   boolean true\n", event: true, shutdown: true},
		// Resuming (false) is not an event; the next true one is.
		"wake then shutdown": {input: "member=PrepareForSleep\n boolean false\nmember=PrepareForShutdown\n boolean true\n", event: true, shutdown: true},
		"unrelated booleans": {input: "method call member=Other\n boolean true\n"},
	}
	for name, tc := range cases {
		events := make(chan bool, 1)
		scanLogindSignals(bufio.NewScanner(strings.NewReader(tc.input)), events)
		shutdown, ok := <-events
		if ok != tc.event || shutdown != tc.shutdown {
			t.Errorf("%s: event %v shutdown %v, want %v %v", name, ok, shutdown, tc.event, tc.shutdown)
		}
	}
}

func TestDaemonResumeContinuesInterruptedLocalWork(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	q := Request{ID: "local-abc123", Root: "/src/demo", Runner: "laptop", UserInterface: "jara", Prompt: "secret first prompt"}
	if err := saveDaemonResume(q, chatDaemonState{Conversation: "th-1", ActiveTurn: true}); err != nil {
		t.Fatal(err)
	}
	if ids := pendingDaemonResumes(); len(ids) != 1 || ids[0] != q.ID {
		t.Fatalf("pending = %v", ids)
	}
	// A window opening another conversation leaves the interrupted one.
	other := Request{ID: q.ID, Resume: true}
	applyDaemonResume(&other)
	if other.Continue != "" || other.Conversation != "" {
		t.Fatalf("resume picker received the interrupted work: %+v", other)
	}
	if err := saveDaemonResume(q, chatDaemonState{Conversation: "th-1", ActiveTurn: true}); err != nil {
		t.Fatal(err)
	}
	opening := Request{ID: q.ID}
	applyDaemonResume(&opening)
	if opening.Conversation != "th-1" || opening.Continue != localContinueNote {
		t.Fatalf("opening the folder = %+v", opening)
	}
	if _, ok := takeDaemonResume(q.ID); ok {
		t.Fatal("interrupted work continued twice")
	}

	// Only scheduled work waiting: the session restarts headless without a note.
	if err := saveDaemonResume(q, chatDaemonState{Conversation: "th-2"}); err != nil {
		t.Fatal(err)
	}
	entry, ok := takeDaemonResume(q.ID)
	if !ok || !entry.Request.Headless || entry.Request.Continue != "" || entry.Request.Prompt != "" || entry.Request.Conversation != "th-2" {
		t.Fatalf("entry = %+v %v", entry, ok)
	}
	if len(entry.Env) == 0 || !filepath.IsAbs(entry.Executable) {
		t.Fatalf("entry cannot be restarted as this client: %+v", entry)
	}
}

func TestChatDaemonReportsWorkInterruptedByAgentServerExit(t *testing.T) {
	interrupted := make(chan chatDaemonState, 1)
	hooks := chatDaemonRunnerHooks{interrupted: func(state chatDaemonState, _ bool) { interrupted <- state }}
	h := startDaemonHarness(t, daemonHarnessOptions{hooks: hooks,
		state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true, ActiveTurn: true}})
	h.assertRunning()
	_ = h.server.out.Close() // the app-server died mid-turn
	select {
	case state := <-interrupted:
		if !state.ActiveTurn || state.Conversation != "th-0" {
			t.Fatalf("interrupted state = %+v", state)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted work was not reported")
	}
	_ = h.wait()
}

func TestChatDaemonIdleExitIsNotInterrupted(t *testing.T) {
	interrupted := make(chan chatDaemonState, 1)
	hooks := chatDaemonRunnerHooks{interrupted: func(state chatDaemonState, _ bool) { interrupted <- state }}
	h := startDaemonHarness(t, daemonHarnessOptions{hooks: hooks, state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true}})
	client, _ := h.attach()
	_ = h.server.out.Close()
	client.expectClosed()
	_ = h.wait()
	select {
	case state := <-interrupted:
		t.Fatalf("an idle session was reported interrupted: %+v", state)
	default:
	}
}
