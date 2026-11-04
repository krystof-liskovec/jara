package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func newChatCommandTestUI(out *bytes.Buffer, wire *bytes.Buffer) *chatUI {
	c := &appConnection{encoder: json.NewEncoder(wire)}
	return &chatUI{c: c, out: out, thread: "thread-test", threadReady: true, pending: map[string]chatPending{}, items: map[string]json.RawMessage{}}
}

func TestChatStatusAndHelpWhileRunning(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.connected = true
	u.busy = true
	u.turn = "turn-test"
	u.startedAt = time.Now().Add(-12 * time.Second)
	u.turnStartedAt = time.Now().Add(-3 * time.Second)
	u.pending["request-1"] = chatPending{method: "turn/start"}
	u.approvals = []appMessage{{
		ID:     json.RawMessage(`"approval-1"`),
		Method: "item/fileChange/requestApproval",
		Params: json.RawMessage(`{"itemId":"file-1","reason":"Apply requested patch","grantRoot":"/workspace","changes":[{"path":"config.toml","diff":"- old\n+ new"}]}`),
	}}
	u.items["file-1"] = json.RawMessage(`{"id":"file-1","type":"fileChange","changes":[{"path":"config.toml","diff":"- old\n+ new"}]}`)

	if _, err := u.line("/help"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("/status"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("/mystery"); err != nil {
		t.Fatal(err)
	}
	if u.currentState() != "waiting" || !u.busy {
		t.Fatalf("help/status changed turn state: busy=%v state=%s", u.busy, u.currentState())
	}
	if wire.Len() != 0 {
		t.Fatalf("commands unexpectedly sent protocol request: %s", wire.String())
	}
	for _, want := range []string{
		"/compose", "/send", "/discard", "Connection: connected", "Session runtime:",
		"State: waiting", "Turn: turn-test", "Pending approvals: 1", "grantRoot",
		"config.toml", "Apply requested patch", "Unknown command: /mystery",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("command output missing %q: %s", want, out.String())
		}
	}
}

func TestChatHandshakeBlocksEarlyPrompt(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.busy = true
	u.pending["1"] = chatPending{method: "initialize"}
	if _, err := u.line("premature prompt"); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 {
		t.Fatalf("prompt sent before thread was ready: %s", wire.String())
	}
	if u.currentState() != "starting" {
		t.Fatalf("state during handshake = %q", u.currentState())
	}
}

func TestChatComposerSendsMultilineMessage(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	for _, line := range []string{"/compose", "first pasted line", "", "  second line  ", "/send"} {
		if _, err := u.line(line); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			Input []struct {
				Text string `json:"text"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(wire.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "turn/start" || len(request.Params.Input) != 1 {
		t.Fatalf("unexpected request: %s", wire.String())
	}
	if want := "first pasted line\n\n  second line  "; request.Params.Input[0].Text != want {
		t.Fatalf("message = %q, want %q", request.Params.Input[0].Text, want)
	}
	if u.composing || u.draftBytes != 0 || len(u.draft) != 0 {
		t.Fatalf("draft state was not cleared after send: %+v", u)
	}
}

func TestChatComposerDiscardAndSizeLimit(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	_, _ = u.line("/compose")
	_, _ = u.line("keep this draft")
	_, _ = u.line(strings.Repeat("x", maxBackgroundPrompt))
	if len(u.draft) != 1 || u.draft[0] != "keep this draft" {
		t.Fatalf("oversized line changed draft: %d lines", len(u.draft))
	}
	_, _ = u.line("/discard")
	if u.composing || len(u.draft) != 0 || u.draftBytes != 0 || wire.Len() != 0 {
		t.Fatalf("discard did not clear draft without sending: %+v wire=%s", u, wire.String())
	}
}

func TestChatApprovalDisplaysRequestAndItemScope(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.items["file-1"] = json.RawMessage(`{"id":"file-1","type":"fileChange","changes":[{"path":"config.toml","diff":"- old\n+ new"}],"comment":"escape \u001b[31m"}`)
	err := u.event(appMessage{
		ID:     json.RawMessage(`"approval-1"`),
		Method: "item/fileChange/requestApproval",
		Params: json.RawMessage(`{"itemId":"file-1","reason":"Apply requested patch","grantRoot":"/workspace"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Request scope:", "grantRoot", "Apply requested patch", "Requested item and changes:", "config.toml", "- old"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("approval output missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("approval rendered a terminal escape: %q", out.String())
	}
}
