package jara

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

func newTestScheduleManager(t *testing.T) *chatScheduleManager {
	t.Helper()
	m, err := newChatScheduleManager(filepath.Join(t.TempDir(), "w.schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// makeDue moves a pending job's run time into the past.
func makeDue(m *chatScheduleManager, id string, age time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.jobs {
		if m.jobs[i].ID == id {
			m.jobs[i].RunAt = time.Now().Add(-age)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

func appResponse(t *testing.T, id string, result any, failure string) appMessage {
	t.Helper()
	message := appMessage{ID: json.RawMessage(jsonString(t, id))}
	if failure != "" {
		message.Error = &struct {
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}{Message: failure}
		return message
	}
	message.Result = json.RawMessage(jsonString(t, result))
	return message
}

func turnCompleted(t *testing.T, turn, status string) appMessage {
	t.Helper()
	return appMessage{Method: "turn/completed", Params: json.RawMessage(jsonString(t, map[string]any{"turn": map[string]string{"id": turn, "status": status}}))}
}

func TestChatScheduleManagerLoadsAndRecoversStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	jobs := []chatSchedule{
		{ID: "sc-run", ThreadID: "th", Prompt: "p", Status: "running", TurnID: "turn"},
		{ID: "sc-wait", ThreadID: "th", Prompt: "p", Status: "pending", RunAt: time.Now().Add(time.Hour)},
	}
	if err := fsutil.WriteJSON(path, jobs); err != nil {
		t.Fatal(err)
	}
	m, err := newChatScheduleManager(path)
	if err != nil {
		t.Fatal(err)
	}
	listed := m.list("th")
	if len(listed) != 2 || listed[0].ID != "sc-run" || listed[0].Status != "failed" || listed[0].TurnID != "" || !strings.Contains(listed[0].Error, "restarted") {
		t.Fatalf("recovered jobs = %+v", listed)
	}
	reloaded, err := newChatScheduleManager(path)
	if err != nil || reloaded.list("")[0].Status != "failed" {
		t.Fatalf("recovery was not saved: %v", err)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("store permissions = %v %v", info, statErr)
	}

	if err = os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = newChatScheduleManager(path); err == nil {
		t.Fatal("corrupt store accepted")
	}
	if _, err = newChatScheduleManager(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory store = %v", err)
	}
	link := filepath.Join(dir, "link.json")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = newChatScheduleManager(link); err == nil {
		t.Fatal("symlinked store accepted")
	}
}

func TestChatScheduleManagerValidatesNewPrompts(t *testing.T) {
	m := newTestScheduleManager(t)
	soon := time.Now().Add(time.Hour)
	for _, test := range []struct {
		prompt string
		runAt  time.Time
		want   string
	}{
		{"  ", soon, "cannot be empty"},
		{strings.Repeat("x", maxBackgroundPrompt+1), soon, "exceeds 64 KiB"},
		{"p", time.Now().Add(-time.Second), "in the future"},
		{"p", time.Now().AddDate(1, 0, 1), "within one year"},
	} {
		if _, err := m.add("th", test.prompt, test.runAt); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("add(%.10q) = %v, want %q", test.prompt, err, test.want)
		}
	}
	job, err := m.add("th", "  trimmed  ", soon)
	if err != nil || job.Prompt != "trimmed" || job.Status != "pending" || !strings.HasPrefix(job.ID, "sc-") || len(job.ID) != 13 {
		t.Fatalf("job = %+v %v", job, err)
	}
	for i := 1; i < chatScheduleLimit; i++ {
		if _, err = m.add("th", "p", soon); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = m.add("th", "p", soon); err == nil || !strings.Contains(err.Error(), "pending scheduled prompts") {
		t.Fatalf("limit = %v", err)
	}
	if _, err = m.cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.add("th", "p", soon); err != nil {
		t.Fatalf("canceled jobs still count toward the limit: %v", err)
	}

	broken := &chatScheduleManager{path: filepath.Join(t.TempDir(), "missing", "s.json")}
	if _, err = broken.add("th", "p", soon); err == nil || len(broken.jobs) != 0 {
		t.Fatalf("unsaved job kept: %v %+v", err, broken.jobs)
	}
}

func TestChatScheduleManagerClientRequests(t *testing.T) {
	m := newTestScheduleManager(t)
	params := func(v any) json.RawMessage { return json.RawMessage(jsonString(t, v)) }
	later := time.Now().Add(2 * time.Hour)
	earlier := time.Now().Add(time.Hour)
	if _, err := m.handleClientRequest("jara/schedule/create", params(map[string]any{"threadId": "", "prompt": "p", "runAt": later}), ""); err == nil {
		t.Fatal("schedule without a conversation accepted")
	}
	if _, err := m.handleClientRequest("jara/schedule/create", json.RawMessage(`{`), "th"); err == nil || !strings.Contains(err.Error(), "invalid schedule request") {
		t.Fatalf("invalid create = %v", err)
	}
	if _, err := m.handleClientRequest("jara/schedule/create", params(map[string]any{"threadId": "th", "prompt": "", "runAt": later}), "th"); err == nil {
		t.Fatal("empty prompt accepted")
	}
	created, err := m.handleClientRequest("jara/schedule/create", params(map[string]any{"threadId": "th", "prompt": "second", "runAt": later}), "th")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := m.add("th", "first", earlier)
	if _, err = m.add("other", "elsewhere", earlier); err != nil {
		t.Fatal(err)
	}
	listed, _ := m.handleClientRequest("jara/schedule/list", nil, "th")
	schedules := listed.(map[string]any)["schedules"].([]chatSchedule)
	if len(schedules) != 2 || schedules[0].ID != first.ID || schedules[1].Prompt != "second" {
		t.Fatalf("list = %+v", schedules)
	}
	if all := m.list(""); len(all) != 3 {
		t.Fatalf("unfiltered list = %+v", all)
	}

	id := created.(map[string]any)["schedule"].(chatSchedule).ID
	canceled, err := m.handleClientRequest("jara/schedule/cancel", params(map[string]string{"id": id}), "th")
	if err != nil || canceled.(map[string]any)["schedule"].(chatSchedule).Status != "canceled" {
		t.Fatalf("cancel = %+v %v", canceled, err)
	}
	if _, err = m.handleClientRequest("jara/schedule/cancel", params(map[string]string{"id": id}), "th"); err == nil || !strings.Contains(err.Error(), "is canceled") {
		t.Fatalf("second cancel = %v", err)
	}
	if _, err = m.handleClientRequest("jara/schedule/cancel", params(map[string]string{"id": "sc-none"}), "th"); err == nil || !strings.Contains(err.Error(), "no scheduled prompt") {
		t.Fatalf("unknown cancel = %v", err)
	}
	if _, err = m.handleClientRequest("jara/schedule/cancel", json.RawMessage(`[`), "th"); err == nil {
		t.Fatal("invalid cancel accepted")
	}
	if _, err = m.handleClientRequest("jara/schedule/delete", nil, "th"); err == nil {
		t.Fatal("unsupported operation accepted")
	}

	if !m.hasPending() {
		t.Fatal("pending jobs not reported")
	}
	if err = m.cancelPending(); err != nil || m.hasPending() {
		t.Fatalf("cancelPending = %v, pending %v", err, m.hasPending())
	}
	if err = m.cancelPending(); err != nil {
		t.Fatal(err)
	}
}

func TestChatScheduleManagerRunsDuePromptsInOrder(t *testing.T) {
	m := newTestScheduleManager(t)
	var input bytes.Buffer
	var inputMu sync.Mutex
	idle := func() bool { return false }
	later, _ := m.add("th", "later", time.Now().Add(time.Hour))
	older, _ := m.add("th", "older", time.Now().Add(time.Hour))
	if err := m.startDue(&input, &inputMu, idle); err != nil || input.Len() != 0 || m.running() {
		t.Fatalf("future job started: %v %q", err, input.String())
	}
	makeDue(m, later.ID, time.Second)
	makeDue(m, older.ID, time.Minute)
	if err := m.startDue(&input, &inputMu, func() bool { return true }); err != nil || input.Len() != 0 {
		t.Fatal("job started while the session was busy")
	}

	if err := m.startDue(&input, &inputMu, idle); err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(input.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	if request["id"] != "jara-schedule-start-"+older.ID || request["method"] != "turn/start" || !strings.Contains(jsonString(t, request["params"]), `"text":"older"`) {
		t.Fatalf("turn request = %v", request)
	}
	if !m.running() || !m.hasPending() {
		t.Fatal("started job not tracked")
	}
	input.Reset()
	if err := m.startDue(&input, &inputMu, idle); err != nil || input.Len() != 0 {
		t.Fatal("second job started while one was running")
	}

	requestID := "jara-schedule-start-" + older.ID
	m.handleAppMessage(appResponse(t, "other-request", map[string]any{"turn": map[string]string{"id": "x"}}, ""))
	m.handleAppMessage(appResponse(t, requestID, map[string]any{"turn": map[string]string{"id": "turn-1"}}, ""))
	m.handleAppMessage(turnCompleted(t, "turn-other", "completed"))
	if !m.running() {
		t.Fatal("another turn's completion finished the schedule")
	}
	m.handleAppMessage(appMessage{Method: "turn/completed", Params: json.RawMessage(`[]`)})
	m.handleAppMessage(turnCompleted(t, "turn-1", "completed"))
	if m.running() {
		t.Fatal("schedule still running after its turn completed")
	}
	jobs := m.list("th")
	if jobs[0].ID != older.ID || jobs[0].Status != "completed" || jobs[0].TurnID != "" {
		t.Fatalf("completed job = %+v", jobs[0])
	}

	// The next due job runs and records an interrupted turn as a failure.
	if err := m.startDue(&input, &inputMu, idle); err != nil {
		t.Fatal(err)
	}
	requestID = "jara-schedule-start-" + later.ID
	m.handleAppMessage(appResponse(t, requestID, map[string]any{"turn": map[string]string{"id": "turn-2"}}, ""))
	m.handleAppMessage(turnCompleted(t, "turn-2", "interrupted"))
	if job := m.list("th")[1]; job.Status != "failed" || job.Error != "scheduled turn ended with status interrupted" {
		t.Fatalf("interrupted job = %+v", job)
	}
	m.handleAppMessage(turnCompleted(t, "turn-2", "completed")) // no active job: ignored
}

func TestChatScheduleManagerStartFailures(t *testing.T) {
	m := newTestScheduleManager(t)
	var inputMu sync.Mutex
	idle := func() bool { return false }
	var input bytes.Buffer
	start := func() string {
		t.Helper()
		job, err := m.add("th", "p", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		makeDue(m, job.ID, time.Second)
		if err = m.startDue(&input, &inputMu, idle); err != nil {
			t.Fatal(err)
		}
		return job.ID
	}
	status := func(id string) chatSchedule {
		for _, job := range m.list("") {
			if job.ID == id {
				return job
			}
		}
		t.Fatalf("job %s missing", id)
		return chatSchedule{}
	}

	id := start()
	m.handleAppMessage(appResponse(t, "jara-schedule-start-"+id, nil, "thread not loaded \x1b[1m"))
	if job := status(id); job.Status != "failed" || !strings.HasPrefix(job.Error, "thread not loaded") || strings.Contains(job.Error, "\x1b") || m.running() {
		t.Fatalf("rejected start = %+v", job)
	}
	id = start()
	m.handleAppMessage(appResponse(t, "jara-schedule-start-"+id, map[string]any{}, ""))
	if job := status(id); job.Status != "failed" || !strings.Contains(job.Error, "did not return a turn ID") {
		t.Fatalf("missing turn ID = %+v", job)
	}

	// Becoming busy between selection and sending defers the job.
	job, _ := m.add("th", "deferred", time.Now().Add(time.Hour))
	makeDue(m, job.ID, time.Second)
	calls := 0
	busyOnSecondCheck := func() bool { calls++; return calls > 1 }
	input.Reset()
	if err := m.startDue(&input, &inputMu, busyOnSecondCheck); err != nil || input.Len() != 0 || m.running() {
		t.Fatalf("deferred start = %v %q", err, input.String())
	}
	if deferred := status(job.ID); deferred.Status != "pending" {
		t.Fatalf("deferred job = %+v", deferred)
	}
	if err := m.deferStart("stale-request"); err != nil {
		t.Fatal(err)
	}

	// A broken app-server pipe fails the job.
	if err := m.startDue(failingWriter{}, &inputMu, idle); err == nil {
		t.Fatal("write failure not reported")
	}
	if failed := status(job.ID); failed.Status != "failed" || failed.Error != "pipe closed" || m.running() {
		t.Fatalf("write failure job = %+v", failed)
	}
	m.finishFailed("stale-request", "ignored")

	// A store that cannot be saved marks the job failed before sending.
	job, _ = m.add("th", "unsaved", time.Now().Add(time.Hour))
	makeDue(m, job.ID, time.Second)
	m.path = filepath.Join(t.TempDir(), "missing", "s.json")
	input.Reset()
	if err := m.startDue(&input, &inputMu, idle); err == nil || input.Len() != 0 {
		t.Fatalf("unsaved start = %v %q", err, input.String())
	}
	if failed := status(job.ID); failed.Status != "failed" || m.running() {
		t.Fatalf("unsaved job = %+v", failed)
	}
	if _, err := m.cancel("sc-none"); err == nil {
		t.Fatal("unknown cancel accepted")
	}
}
