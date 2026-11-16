package jara

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionQueueRunsInOrderAndNotifies(t *testing.T) {
	m, err := newChatScheduleManager(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var notices [][]byte
	m.onQueue = func(line []byte) { notices = append(notices, line) }
	request := func(method string, params map[string]string) error {
		raw, _ := json.Marshal(params)
		_, err := m.handleQueueRequest(method, raw, "th-1")
		return err
	}
	for _, prompt := range []string{"first", "second", "third"} {
		if err = request("jara/queue/add", map[string]string{"threadId": "th-1", "prompt": prompt}); err != nil {
			t.Fatal(err)
		}
	}
	if err = request("jara/queue/add", map[string]string{"threadId": "th-2", "prompt": "elsewhere"}); err == nil {
		t.Fatal("queued into a conversation the session does not show")
	}
	queued := m.queuedPrompts()
	if err = request("jara/queue/update", map[string]string{"id": queued[1].ID, "prompt": "second, edited"}); err != nil {
		t.Fatal(err)
	}
	if err = request("jara/queue/remove", map[string]string{"id": queued[2].ID}); err != nil {
		t.Fatal(err)
	}
	if len(m.list("")) != 0 {
		t.Fatal("queued prompts appear among scheduled prompts")
	}
	if last := string(notices[len(notices)-1]); !strings.Contains(last, "second, edited") || strings.Contains(last, "third") {
		t.Fatalf("queue notice = %s", last)
	}

	var input bytes.Buffer
	var mu sync.Mutex
	idle := func() bool { return false }
	if err = m.startDue(&input, &mu, idle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.String(), `"text":"first"`) {
		t.Fatalf("first queued prompt did not start: %s", input.String())
	}
	if remaining := m.queuedPrompts(); len(remaining) != 1 || remaining[0].Prompt != "second, edited" {
		t.Fatalf("queue after start = %+v", remaining)
	}
	if err = request("jara/queue/update", map[string]string{"id": queued[0].ID, "prompt": "late"}); err == nil {
		t.Fatal("edited a queued prompt that already started")
	}
	// The queue moves with the conversation like scheduled prompts.
	if pending := pendingSchedules(m.path); len(pending) != 1 || !pending[0].Queued {
		t.Fatalf("pending prompts to move = %+v", pending)
	}
}

func TestCrunchTaskMovesAndStaleTasksAreCleared(t *testing.T) {
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "a.crunch.json"), filepath.Join(dir, "b.crunch.json")
	job := chatCrunchJob{ID: "c-1", ThreadID: "th-1", Mode: "crunch", Prompt: "keep going", Status: "running", CreatedAt: time.Now()}
	if err := installCrunch(source, mustJSON(t, job)); err != nil {
		t.Fatal(err)
	}
	task := activeCrunch(source)
	if task == nil {
		t.Fatal("running crunch task was not exported")
	}
	if err := installCrunch(dest, task); err != nil {
		t.Fatal(err)
	}
	m, err := newChatCrunchManager(dest)
	if err != nil || !m.hasPending() || !m.job.Started || m.job.Status != "pending" {
		t.Fatalf("moved crunch task = %+v %v", m.job, err)
	}
	if err = installCrunch(dest, nil); err != nil || activeCrunch(dest) != nil {
		t.Fatalf("stale crunch task kept: %v", err)
	}
}

func TestWindowMirrorsSessionQueue(t *testing.T) {
	u := &chatUI{}
	u.queuedPrompts = []chatQueuedPrompt{u.newQueuedPrompt("th-1", "typed offline")}
	update := func(prompts ...map[string]string) {
		raw, _ := json.Marshal(map[string]any{"prompts": prompts})
		u.acceptQueueUpdate(raw)
	}
	update(map[string]string{"id": "q-1", "threadId": "th-1", "prompt": "one"})
	editing := u.queuedPrompts[0].id
	update(map[string]string{"id": "q-1", "threadId": "th-1", "prompt": "one"}, map[string]string{"id": "q-2", "threadId": "th-1", "prompt": "two"})
	if len(u.queuedPrompts) != 3 || u.queuedPrompts[0].id != editing || u.queuedPrompts[2].session != "" {
		t.Fatalf("mirrored queue = %+v", u.queuedPrompts)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
