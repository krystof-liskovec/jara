package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type capturedChatRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		CWD      string `json:"cwd"`
		ThreadID string `json:"threadId"`
	} `json:"params"`
}

func decodeChatRequests(t *testing.T, wire *bytes.Buffer) []capturedChatRequest {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(wire.Bytes()))
	var requests []capturedChatRequest
	for {
		var request capturedChatRequest
		if err := decoder.Decode(&request); err != nil {
			break
		}
		requests = append(requests, request)
	}
	return requests
}

func TestChatSessionsResponseAndNumericResumeUseExactThread(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.connected = true
	u.cwd = "/workspace/current"
	if _, err := u.line("/sessions build"); err != nil {
		t.Fatal(err)
	}
	requests := decodeChatRequests(t, &wire)
	if len(requests) != 1 || requests[0].Method != "thread/list" || requests[0].Params.CWD != u.cwd {
		t.Fatalf("session listing did not issue a scoped protocol request: %+v", requests)
	}
	if len(u.pending) != 1 || !strings.Contains(output.String(), "Searching saved sessions") {
		t.Fatalf("session listing should return while request is pending: pending=%v output=%q", u.pending, output.String())
	}
	page := chatSessionPageJSON(t, []Conversation{
		{ID: "saved-1", Name: "Build notes", CWD: u.cwd},
		{ID: "saved-2", Name: "Build implementation", CWD: u.cwd},
	}, "")
	if err := u.response(appMessage{ID: requests[0].ID, Result: page}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1. Build notes") || !strings.Contains(output.String(), "2. Build implementation") {
		t.Fatalf("numbered choices were not displayed: %q", output.String())
	}
	if u.busy || u.thread != "thread-test" {
		t.Fatalf("listing changed the active conversation: busy=%v thread=%q", u.busy, u.thread)
	}
	if _, err := u.line("/resume 2"); err != nil {
		t.Fatal(err)
	}
	requests = decodeChatRequests(t, &wire)
	if len(requests) != 2 || requests[1].Method != "thread/resume" || requests[1].Params.ThreadID != "saved-2" {
		t.Fatalf("numeric resume did not resolve the displayed second choice: %+v", requests)
	}
	if !u.busy {
		t.Fatal("resuming the selected session should enter startup state")
	}
}

func TestChatSessionListLeavesRunningTurnBusy(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.connected = true
	u.cwd = "/workspace/current"
	u.busy = true
	u.turn = "turn-current"
	u.turnStartedAt = time.Now()
	if _, err := u.line("/sessions"); err != nil {
		t.Fatal(err)
	}
	requests := decodeChatRequests(t, &wire)
	if len(requests) != 1 || requests[0].Method != "thread/list" {
		t.Fatalf("busy turn should still permit asynchronous listing: %+v", requests)
	}
	page := chatSessionPageJSON(t, []Conversation{{ID: "saved-1", Name: "Saved", CWD: u.cwd}}, "")
	if err := u.response(appMessage{ID: requests[0].ID, Result: page}); err != nil {
		t.Fatal(err)
	}
	if !u.busy || u.turn != "turn-current" || u.currentState() != "running" {
		t.Fatalf("session response disturbed running turn: busy=%v turn=%q state=%q", u.busy, u.turn, u.currentState())
	}
	before := wire.Len()
	if _, err := u.line("/resume 1"); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != before {
		t.Fatalf("resuming while busy should not send a request: %s", wire.String())
	}
	if !u.busy || u.turn != "turn-current" {
		t.Fatalf("blocked resume changed active turn: busy=%v turn=%q", u.busy, u.turn)
	}
}

func TestNewChatSessionQueryInvalidatesOlderRequestAndChoices(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.connected = true
	u.cwd = "/workspace/current"
	_, _ = u.line("/sessions first")
	requests := decodeChatRequests(t, &wire)
	_, _ = u.line("/sessions second")
	requests = decodeChatRequests(t, &wire)
	if len(requests) != 2 || len(u.pending) != 1 {
		t.Fatalf("new query should replace prior in-flight request: requests=%+v pending=%v", requests, u.pending)
	}
	oldPage := chatSessionPageJSON(t, []Conversation{{ID: "old-choice", Name: "First query", CWD: u.cwd}}, "")
	if err := u.response(appMessage{ID: requests[0].ID, Result: oldPage}); err != nil {
		t.Fatal(err)
	}
	if u.sessions.query != "second" || len(u.sessions.matches) != 0 {
		t.Fatalf("stale response replaced current session choices: %+v", u.sessions)
	}
	newPage := chatSessionPageJSON(t, []Conversation{{ID: "new-choice", Name: "Second query", CWD: u.cwd}}, "")
	if err := u.response(appMessage{ID: requests[1].ID, Result: newPage}); err != nil {
		t.Fatal(err)
	}
	if id, err := u.sessions.resumeChoice("1"); err != nil || id != "new-choice" {
		t.Fatalf("numeric choice used stale results: id=%q err=%v", id, err)
	}
}

func TestChatResumeWithoutArgumentLoadsRecentSessions(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.connected = true
	u.cwd = "/workspace/current"
	if _, err := u.line("/resume"); err != nil {
		t.Fatal(err)
	}
	requests := decodeChatRequests(t, &wire)
	if len(requests) != 1 || requests[0].Method != "thread/list" || requests[0].Params.CWD != u.cwd {
		t.Fatalf("bare /resume did not request recent workspace sessions: %+v", requests)
	}
	page := chatSessionPageJSON(t, []Conversation{
		{ID: "older", Name: "Older session", CWD: u.cwd, Updated: 1},
		{ID: "newer", Name: "Newer session", CWD: u.cwd, Updated: 2},
	}, "")
	if err := u.response(appMessage{ID: requests[0].ID, Result: page}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1. Newer session") || !strings.Contains(output.String(), "2. Older session") {
		t.Fatalf("recent sessions were not displayed newest first: %q", output.String())
	}
}

func TestChatSessionListErrorsAndTimeoutStayNonfatal(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.connected = true
	u.cwd = "/workspace/current"
	_, _ = u.line("/sessions")
	requests := decodeChatRequests(t, &wire)
	errorMessage := "history unavailable"
	if err := u.response(appMessage{ID: requests[0].ID, Error: &struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}{Message: errorMessage}}); err != nil {
		t.Fatalf("history error should not end the chat: %v", err)
	}
	if u.busy || !u.sessions.complete || !u.sessions.incomplete || !strings.Contains(output.String(), errorMessage) {
		t.Fatalf("history error was not reported as incomplete: busy=%v sessions=%+v output=%q", u.busy, u.sessions, output.String())
	}

	output.Reset()
	_, _ = u.line("/sessions retry")
	var requestID string
	for id, pending := range u.pending {
		if pending.method == "thread/list" {
			requestID = id
		}
	}
	if requestID == "" || !u.expireChatSessionRequest(requestID) {
		t.Fatal("thread/list timeout was not handled nonfatally")
	}
	if _, pending := u.pending[requestID]; pending || !strings.Contains(output.String(), "incomplete") || !strings.Contains(output.String(), "timed out") {
		t.Fatalf("timeout state/output incorrect: pending=%v output=%q", u.pending, output.String())
	}
}
