package jara

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRecoverChatRequestErrorsRestoresUsableState(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output bytes.Buffer
	u := &chatUI{out: &output, thread: "thread-old", threadReady: true, busy: true, cancel: true, turn: "turn-pending", approvals: []appMessage{{}}, questionRequests: []chatQuestionRequest{{}}, items: map[string]json.RawMessage{"item": json.RawMessage(`{}`)}}
	if !recoverChatRequestError(u, chatPending{method: "turn/start"}, appMessage{Error: &struct {
		Message string `json:"message"`
	}{Message: "request rejected\x1b[31m"}}) {
		t.Fatal("turn/start error was not recovered")
	}
	if u.busy || u.cancel || u.turn != "" || !u.threadReady || u.thread != "thread-old" || len(u.approvals) != 0 || len(u.questionRequests) != 0 || len(u.items) != 0 {
		t.Fatalf("failed turn left inconsistent state: %+v", u)
	}
	if !strings.Contains(output.String(), "Could not start turn: request rejected") || strings.Contains(output.String(), "\x1b") {
		t.Fatalf("failure was not safely displayed: %q", output.String())
	}

	u.busy = true
	if !recoverChatRequestError(u, chatPending{method: "thread/resume"}, appMessage{Error: &struct {
		Message string `json:"message"`
	}{Message: "resume rejected"}}) {
		t.Fatal("thread/resume error was not recovered")
	}
	if u.busy || !u.threadReady || u.thread != "thread-old" || u.currentState() != "idle" {
		t.Fatalf("failed resume did not restore prior conversation: %+v", u)
	}
	if !strings.Contains(output.String(), "Continuing previous conversation.") {
		t.Fatalf("resume failure did not explain fallback: %q", output.String())
	}

	u.busy = true
	if !recoverChatRequestError(u, chatPending{method: "thread/start"}, appMessage{Error: &struct {
		Message string `json:"message"`
	}{Message: "new conversation rejected"}}) {
		t.Fatal("thread/start error was not recovered")
	}
	if u.busy || !u.threadReady || u.thread != "thread-old" {
		t.Fatalf("failed new conversation did not preserve old thread: %+v", u)
	}

	u.thread, u.threadReady, u.busy = "missing-thread", false, true
	if !recoverChatRequestError(u, chatPending{method: "thread/resume"}, appMessage{Error: &struct {
		Message string `json:"message"`
	}{Message: "not found"}}) {
		t.Fatal("initial thread/resume error was not recovered")
	}
	if u.thread != "" || u.threadReady || u.busy || !strings.Contains(output.String(), "Use /resume ID or /new") {
		t.Fatalf("failed initial resume retained unusable thread state: %+v output=%q", u, output.String())
	}

	if recoverChatRequestError(u, chatPending{method: "initialize"}, appMessage{Error: &struct {
		Message string `json:"message"`
	}{Message: "handshake failed"}}) {
		t.Fatal("initialize failure must remain fatal")
	}
}

func TestPromptWithoutConversationDoesNotSendOrDiscardDraft(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := &chatUI{c: &appConnection{encoder: json.NewEncoder(&wire)}, out: &output, thread: "missing-thread", pending: map[string]chatPending{}}
	if err := u.sendPrompt("must not run without a thread"); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || u.busy {
		t.Fatalf("prompt was sent without an open conversation: busy=%v wire=%q", u.busy, wire.String())
	}
	if _, err := u.line("/compose"); err != nil {
		t.Fatal(err)
	}
	if u.composing {
		t.Fatal("composer opened before a conversation was ready")
	}
	u.composing = true
	u.draft = []string{"keep this draft"}
	u.draftBytes = len(u.draft[0])
	if err := u.sendDraft(); err != nil {
		t.Fatal(err)
	}
	if !u.composing || len(u.draft) != 1 || u.draft[0] != "keep this draft" || u.draftBytes == 0 {
		t.Fatalf("draft changed without an open conversation: %+v", u)
	}
	if wire.Len() != 0 || !strings.Contains(output.String(), "Your draft is still here") {
		t.Fatalf("draft refusal was not clear: output=%q wire=%q", output.String(), wire.String())
	}
}

// The subprocess fixture exercises recovery through actual app-server pipes.
func TestChatRecoveryAppServerFixture(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "jara-chat-recovery-fixture" {
		return
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	turnStarts := 0
	send := func(value any) {
		if err := enc.Encode(value); err != nil {
			os.Exit(2)
		}
	}
	for {
		var message appMessage
		if dec.Decode(&message) != nil {
			os.Exit(0)
		}
		switch message.Method {
		case "initialize":
			send(map[string]any{"id": message.ID, "result": map[string]any{}})
		case "initialized":
		case "thread/start":
			send(map[string]any{"id": message.ID, "result": map[string]any{"thread": map[string]string{"id": "thread-old"}}})
		case "thread/resume":
			send(map[string]any{"id": message.ID, "error": map[string]any{"code": -32001, "message": "saved conversation unavailable"}})
		case "turn/start":
			turnStarts++
			var params struct {
				ThreadID string `json:"threadId"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.ThreadID != "thread-old" {
				os.Exit(3)
			}
			if turnStarts == 1 {
				send(map[string]any{"id": message.ID, "error": map[string]any{"code": -32002, "message": "turn rejected"}})
				continue
			}
			send(map[string]any{"id": message.ID, "result": map[string]any{"turn": map[string]string{"id": "turn-after-recovery"}}})
			send(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]string{"id": "turn-after-recovery", "status": "completed"}}})
		default:
			os.Exit(4)
		}
	}
}

func TestChatContinuesAfterRejectedResume(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	c, err := startChatServer(os.Args[0], []string{"-test.run=^TestChatRecoveryAppServerFixture$", "jara-chat-recovery-fixture"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	output := chatTestOutput{writes: make(chan string, 64)}
	done := make(chan error, 1)
	go func() { done <- chatLoop(c, reader, output, "/workspace", "") }()
	var transcript strings.Builder
	wait := func(needle string) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for !strings.Contains(transcript.String(), needle) {
			select {
			case text := <-output.writes:
				transcript.WriteString(text)
			case err := <-done:
				t.Fatalf("chat ended before %q: %v; transcript=%q", needle, err, transcript.String())
			case <-timer.C:
				t.Fatalf("missing %q in %q", needle, transcript.String())
			}
		}
	}
	send := func(line string) {
		t.Helper()
		if _, err := io.WriteString(writer, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	wait("You> ")
	send("/resume missing-thread")
	wait("Could not resume conversation: saved conversation unavailable")
	wait("Continuing previous conversation.")
	send("Try a turn that will be rejected")
	wait("Could not start turn: turn rejected")
	send("Continue on the current thread")
	wait("[completed]")
	if !strings.Contains(transcript.String(), "Could not resume conversation") {
		t.Fatalf("recovery failure missing from transcript: %q", transcript.String())
	}
	send("/quit")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit after recovery")
	}
}
