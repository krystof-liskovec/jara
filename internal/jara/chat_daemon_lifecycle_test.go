package jara

import (
	"strings"
	"testing"
	"time"
)

func TestChatDaemonAnswersLocalRequestsWithoutAppServer(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0"}})
	client, _ := h.attach()

	client.send(map[string]any{"id": 1, "method": "jara/shell", "params": map[string]string{"command": "printf hello"}})
	if shell := client.next(); shell["id"] != float64(1) || jsonString(t, shell["result"]) != `{"exitCode":0,"output":"hello"}` {
		t.Fatalf("shell response = %v", shell)
	}

	client.send(map[string]any{"id": 2, "method": "jara/schedule/create", "params": map[string]any{"threadId": "other", "prompt": "x", "runAt": time.Now().Add(time.Hour)}})
	if failed := client.next(); failed["id"] != float64(2) || !strings.Contains(jsonString(t, failed["error"]), "conversation changed") {
		t.Fatalf("schedule for another thread = %v", failed)
	}
	client.send(map[string]any{"id": 3, "method": "jara/schedule/create", "params": map[string]any{"threadId": "th-0", "prompt": "later", "runAt": time.Now().Add(time.Hour)}})
	created := client.next()
	schedule, _ := created["result"].(map[string]any)["schedule"].(map[string]any)
	if schedule["status"] != "pending" || schedule["prompt"] != "later" {
		t.Fatalf("schedule create = %v", created)
	}
	client.send(map[string]any{"id": 4, "method": "jara/schedule/list"})
	if listed := client.next(); !strings.Contains(jsonString(t, listed["result"]), schedule["id"].(string)) {
		t.Fatalf("schedule list = %v", listed)
	}
	client.send(map[string]any{"id": 5, "method": "jara/schedule/cancel", "params": map[string]any{"id": schedule["id"]}})
	if canceled := client.next(); !strings.Contains(jsonString(t, canceled["result"]), `"status":"canceled"`) {
		t.Fatalf("schedule cancel = %v", canceled)
	}

	client.send(map[string]any{"id": 6, "method": "jara/crunch/status"})
	if status := client.next(); jsonString(t, status["result"]) != `{"job":null}` {
		t.Fatalf("crunch status = %v", status)
	}
	client.send(map[string]any{"id": 7, "method": "jara/crunch/create", "params": map[string]any{"threadId": "th-0", "mode": "prompt", "prompt": "grind"}})
	if created := client.next(); !strings.Contains(jsonString(t, created["result"]), `"status":"pending"`) {
		t.Fatalf("crunch create = %v", created)
	}
	client.send(map[string]any{"id": 8, "method": "jara/crunch/stop", "params": map[string]string{"threadId": "th-0"}})
	if stopped := client.next(); !strings.Contains(jsonString(t, stopped["result"]), `"status":"stopped"`) {
		t.Fatalf("crunch stop = %v", stopped)
	}
	client.send(map[string]any{"id": 9, "method": "jara/crunch/stop"})
	if again := client.next(); !strings.Contains(jsonString(t, again["error"]), "no crunch task is running") {
		t.Fatalf("second crunch stop = %v", again)
	}

	// None of the local requests reached the app-server.
	client.send(map[string]any{"id": 10, "method": "ping/check"})
	if first := h.server.next(); first["method"] != "ping/check" {
		t.Fatalf("local request leaked to app-server: %v", first)
	}
}

func TestChatDaemonCloseKeepsServingOtherClients(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{})
	first, _ := h.attach()
	second, _ := h.attach()
	h.waitState("two clients", func(s chatDaemonState) bool { return s.AttachedClients == 2 })
	first.send(map[string]string{"method": "jara/close"})
	first.expectClosed()
	h.waitState("one client", func(s chatDaemonState) bool { return s.AttachedClients == 1 })
	h.assertRunning()
	second.send(map[string]any{"id": 1, "method": "ping/check"})
	if response := second.next(); response["id"] != float64(1) {
		t.Fatalf("remaining client response = %v", response)
	}
	second.send(map[string]string{"method": "jara/close"})
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestChatDaemonDisconnectKeepsSessionUntilHandoff(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{})
	client, _ := h.attach()
	h.waitState("attached", func(s chatDaemonState) bool { return s.Attached })
	_ = client.conn.Close()
	h.waitState("detached", func(s chatDaemonState) bool { return !s.Attached })
	h.assertRunning()

	again, _ := h.attach()
	again.send(map[string]string{"method": "jara/handoff"})
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestChatDaemonHandoffWaitsForTurnButCloseEndsSession(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true}})
	client, _ := h.attach()
	client.send(map[string]any{"id": 1, "method": "turn/start", "params": map[string]any{"threadId": "th-0"}})
	h.server.nextMethod("turn/start")
	client.next() // turn/start response
	if started := client.next(); started["method"] != "turn/started" {
		t.Fatalf("turn notification = %v", started)
	}
	client.send(map[string]string{"method": "jara/handoff"}) // refused while the turn runs
	client.expectClosed()
	h.assertRunning()

	// The UI sends jara/detach while a turn runs; jara/close ends the session.
	closer, ready := h.attach()
	if !ready.ActiveTurn {
		t.Fatalf("reattach ready = %+v", ready)
	}
	closer.send(map[string]string{"method": "jara/close"})
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
	if state := h.state(); state.ActiveTurn || state.Attached {
		t.Fatalf("final state = %+v", state)
	}
}

func TestChatDaemonWithoutClientsExitsWhenTurnCompletes(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true, ActiveTurn: true}})
	h.assertRunning()
	h.server.emit(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "completed"}}})
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestChatDaemonStopCancelsScheduledAndCrunchWork(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0"}})
	if _, err := h.schedules.add("th-0", "later", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.crunch.create("th-0", "goal", "finish it"); err != nil {
		t.Fatal(err)
	}
	client, _ := h.attach()
	client.send(map[string]string{"method": "jara/close"})
	client.expectClosed()
	h.waitState("detached", func(s chatDaemonState) bool { return !s.Attached })
	h.assertRunning() // pending work keeps the daemon alive

	stopper, _ := h.attach()
	stopper.send(map[string]string{"method": "jara/stop"})
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
	pause := h.server.nextMethod("thread/goal/set")
	if jsonString(t, pause["params"]) != `{"status":"paused","threadId":"th-0"}` {
		t.Fatalf("goal pause = %v", pause)
	}
	reloaded, err := newChatScheduleManager(h.paths.schedules)
	if err != nil {
		t.Fatal(err)
	}
	if jobs := reloaded.list(""); len(jobs) != 1 || jobs[0].Status != "canceled" {
		t.Fatalf("schedules after stop = %+v", jobs)
	}
	crunch, err := newChatCrunchManager(h.paths.crunch)
	if err != nil {
		t.Fatal(err)
	}
	if job := crunch.current(); job == nil || job.Status != "stopped" {
		t.Fatalf("crunch after stop = %+v", job)
	}
}

func TestChatDaemonReportsAppServerExit(t *testing.T) {
	for _, test := range []struct {
		status int
		want   string
	}{{0, ""}, {3, "Codex app-server stopped: exit status 3"}} {
		h := startDaemonHarness(t, daemonHarnessOptions{exitStatus: test.status})
		client, _ := h.attach()
		_ = h.server.out.Close()
		err := h.wait()
		if (err == nil) != (test.want == "") || (err != nil && err.Error() != test.want) {
			t.Fatalf("exit status %d returned %v, want %q", test.status, err, test.want)
		}
		client.expectClosed()
	}
}

func TestChatDaemonReportsListenerFailure(t *testing.T) {
	h := startDaemonHarness(t, daemonHarnessOptions{})
	_ = h.listener.Close()
	if err := h.wait(); err == nil || !strings.Contains(err.Error(), "chat session listener stopped") {
		t.Fatalf("listener failure returned %v", err)
	}
}

func TestChatDaemonRunsDueScheduleThenExits(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the daemon's one-second schedule tick")
	}
	var scheduleID string
	h := startDaemonHarness(t, daemonHarnessOptions{
		state: chatDaemonState{Conversation: "th-0", ThreadLoaded: true},
		setup: func(h *daemonHarness) {
			job, err := h.schedules.add("th-0", "scheduled work", time.Now().Add(20*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			scheduleID = job.ID
		},
	})
	h.server.setHandler(func(f *fakeAppServer, message map[string]any) {
		if message["method"] != "turn/start" {
			return
		}
		f.emit(map[string]any{"id": message["id"], "result": map[string]any{"turn": map[string]any{"id": "sched-turn"}}})
		f.emit(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]any{"id": "sched-turn"}}})
		f.emit(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "sched-turn", "status": "completed"}}})
	})
	start := h.server.nextMethod("turn/start")
	if start["id"] != "jara-schedule-start-"+scheduleID || !strings.Contains(jsonString(t, start["params"]), `"text":"scheduled work"`) {
		t.Fatalf("scheduled turn = %v", start)
	}
	select {
	case err := <-h.done:
		h.done <- err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("idle daemon did not exit after its scheduled work")
	}
	if jobs := h.schedules.list("th-0"); len(jobs) != 1 || jobs[0].Status != "completed" {
		t.Fatalf("schedule after run = %+v", jobs)
	}
}
