package jara

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestCrunchManager(t *testing.T, job *chatCrunchJob) *chatCrunchManager {
	t.Helper()
	m := &chatCrunchManager{path: filepath.Join(t.TempDir(), "w.crunch.json"), job: job}
	if job != nil {
		if job.ID == "" {
			job.ID = "cr-test"
		}
		if job.ThreadID == "" {
			job.ThreadID = "th"
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := m.saveLocked(); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func crunchLines(t *testing.T, input *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(input.String()), "\n") {
		if line == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("invalid crunch line %q", line)
		}
		out = append(out, message)
	}
	input.Reset()
	return out
}

func savedCrunchJob(t *testing.T, m *chatCrunchManager) *chatCrunchJob {
	t.Helper()
	var job chatCrunchJob
	data, err := os.ReadFile(m.path)
	if err == nil {
		err = json.Unmarshal(data, &job)
	}
	if err != nil {
		t.Fatal(err)
	}
	return &job
}

func TestChatCrunchStopValidatesOwnership(t *testing.T) {
	var input bytes.Buffer
	var inputMu sync.Mutex
	if err := newTestCrunchManager(t, nil).stop(&input, &inputMu, "th"); err == nil || !strings.Contains(err.Error(), "no crunch task") {
		t.Fatalf("stop without job = %v", err)
	}
	finished := newTestCrunchManager(t, &chatCrunchJob{Mode: "prompt", Status: "completed"})
	if err := finished.stop(&input, &inputMu, "th"); err == nil {
		t.Fatal("finished job stopped again")
	}
	m := newTestCrunchManager(t, &chatCrunchJob{Mode: "prompt", Status: "pending"})
	if err := m.stop(&input, &inputMu, "other"); err == nil || !strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("foreign stop = %v", err)
	}
	// An empty current thread (no conversation open) may still stop the job.
	if err := m.stop(&input, &inputMu, ""); err != nil {
		t.Fatal(err)
	}
	if job := savedCrunchJob(t, m); job.Status != "stopped" || m.hasPending() || input.Len() != 0 {
		t.Fatalf("stopped pending job = %+v, wrote %q", job, input.String())
	}
}

func TestChatCrunchStopInterruptsRunningGoalTurn(t *testing.T) {
	var input bytes.Buffer
	var inputMu sync.Mutex
	m := newTestCrunchManager(t, &chatCrunchJob{Mode: "goal", Status: "running", TurnID: "turn-1", Error: "old"})
	if err := m.stop(&input, &inputMu, "th"); err != nil {
		t.Fatal(err)
	}
	lines := crunchLines(t, &input)
	if len(lines) != 2 || lines[0]["method"] != "thread/goal/set" || jsonString(t, lines[0]["params"]) != `{"status":"paused","threadId":"th"}` {
		t.Fatalf("goal pause = %v", lines)
	}
	if lines[1]["method"] != "turn/interrupt" || jsonString(t, lines[1]["params"]) != `{"threadId":"th","turnId":"turn-1"}` {
		t.Fatalf("interrupt = %v", lines[1])
	}
	if lines[0]["id"] == lines[1]["id"] || !strings.HasPrefix(lines[0]["id"].(string), "jara-crunch-cr-test-") {
		t.Fatalf("request IDs = %v %v", lines[0]["id"], lines[1]["id"])
	}
	job := savedCrunchJob(t, m)
	if job.Status != "stopping" || job.Error != "" || !m.running() || !m.hasPending() {
		t.Fatalf("stopping job = %+v", job)
	}

	// The interrupted turn completes and the job settles as stopped.
	m.handleAppMessage(turnCompleted(t, "turn-1", "interrupted"), &input, &inputMu, func() bool { return false })
	if current := m.current(); current.Status != "stopped" || m.hasPending() {
		t.Fatalf("after interrupt = %+v", current)
	}
	if lines = crunchLines(t, &input); len(lines) != 1 || lines[0]["method"] != "thread/goal/set" {
		t.Fatalf("follow-up pause = %v", lines)
	}

	// Stopping a job that is already stopping finishes it immediately.
	again := newTestCrunchManager(t, &chatCrunchJob{Mode: "prompt", Status: "stopping", TurnID: "turn-2"})
	if err := again.stop(&input, &inputMu, "th"); err != nil {
		t.Fatal(err)
	}
	if current := again.current(); current.Status != "stopped" {
		t.Fatalf("second stop = %+v", current)
	}
	if lines = crunchLines(t, &input); len(lines) != 1 || lines[0]["method"] != "turn/interrupt" {
		t.Fatalf("second stop wrote %v", lines)
	}
}

func TestChatCrunchStopBeforeTurnStartsInterruptsOnceTurnIsKnown(t *testing.T) {
	var input bytes.Buffer
	var inputMu sync.Mutex
	idle := func() bool { return false }
	m := newTestCrunchManager(t, &chatCrunchJob{Mode: "prompt", Prompt: "work", Status: "pending", CreatedAt: time.Now()})
	if err := m.tick(&input, &inputMu, idle); err != nil {
		t.Fatal(err)
	}
	start := crunchLines(t, &input)
	if len(start) != 1 || start[0]["method"] != "turn/start" {
		t.Fatalf("crunch start = %v", start)
	}
	if err := m.stop(&input, &inputMu, "th"); err != nil {
		t.Fatal(err)
	}
	if lines := crunchLines(t, &input); len(lines) != 0 {
		t.Fatalf("interrupt sent before the turn ID was known: %v", lines)
	}
	m.handleAppMessage(appResponse(t, start[0]["id"].(string), map[string]any{"turn": map[string]string{"id": "turn-9"}}, ""), &input, &inputMu, idle)
	lines := crunchLines(t, &input)
	if len(lines) != 1 || lines[0]["method"] != "turn/interrupt" || jsonString(t, lines[0]["params"]) != `{"threadId":"th","turnId":"turn-9"}` {
		t.Fatalf("deferred interrupt = %v", lines)
	}
	m.handleAppMessage(turnCompleted(t, "turn-9", "completed"), &input, &inputMu, idle)
	if current := m.current(); current.Status != "stopped" {
		t.Fatalf("stopped turn finished as %+v", current)
	}
}

func TestChatCrunchStopReportsFailures(t *testing.T) {
	var inputMu sync.Mutex
	m := newTestCrunchManager(t, &chatCrunchJob{Mode: "prompt", Status: "running", TurnID: "turn-1"})
	if err := m.stop(failingWriter{}, &inputMu, "th"); err == nil {
		t.Fatal("interrupt write failure not reported")
	}
	goal := newTestCrunchManager(t, &chatCrunchJob{Mode: "goal", Status: "running", TurnID: "turn-1"})
	if err := goal.stop(failingWriter{}, &inputMu, "th"); err == nil {
		t.Fatal("goal pause write failure not reported")
	}
	unsaved := newTestCrunchManager(t, &chatCrunchJob{Mode: "prompt", Status: "pending"})
	unsaved.path = filepath.Join(t.TempDir(), "missing", "c.json")
	var input bytes.Buffer
	if err := unsaved.stop(&input, &inputMu, "th"); err == nil || input.Len() != 0 {
		t.Fatalf("unsaved stop = %v, wrote %q", err, input.String())
	}
}

func TestChatCrunchStopAll(t *testing.T) {
	var input bytes.Buffer
	var inputMu sync.Mutex
	newTestCrunchManager(t, nil).stopAll(&input, &inputMu)
	newTestCrunchManager(t, &chatCrunchJob{Mode: "goal", Status: "failed"}).stopAll(&input, &inputMu)
	if input.Len() != 0 {
		t.Fatalf("inactive jobs wrote %q", input.String())
	}

	m := newTestCrunchManager(t, &chatCrunchJob{Mode: "goal", Status: "checkingGoal", TurnID: "turn-3", Error: "x"})
	m.requestID, m.requestKind = "jara-crunch-cr-test-1", "goalGet"
	m.stopAll(&input, &inputMu)
	lines := crunchLines(t, &input)
	if len(lines) != 2 || lines[0]["method"] != "thread/goal/set" || lines[1]["method"] != "turn/interrupt" {
		t.Fatalf("stopAll wrote %v", lines)
	}
	job := savedCrunchJob(t, m)
	if job.Status != "stopped" || job.Error != "" || m.requestID != "" || m.requestKind != "" || m.hasPending() {
		t.Fatalf("stopAll job = %+v request %q", job, m.requestID)
	}

	prompt := newTestCrunchManager(t, &chatCrunchJob{Mode: "prompt", Status: "waitingUsage"})
	prompt.stopAll(&input, &inputMu)
	if input.Len() != 0 || prompt.current().Status != "stopped" {
		t.Fatalf("idle prompt stopAll = %q %+v", input.String(), prompt.current())
	}
}
