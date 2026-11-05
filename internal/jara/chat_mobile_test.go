//go:build linux || darwin

package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

const (
	chatMobileChildEnv = "JARA_CHAT_MOBILE_CHILD"
	chatMobileFixture  = "jara-chat-mobile-fixture"
	chatMobilePrompt   = "Summarize\nthis pasted block"
)

// The helper runs chatLoop as a process attached to a real 40x20 PTY.
func TestChatMobilePTYChild(t *testing.T) {
	if os.Getenv(chatMobileChildEnv) != "1" {
		return
	}
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if size.Cols != 40 || size.Rows != 20 {
		t.Fatalf("child PTY size = %dx%d, want 40x20", size.Cols, size.Rows)
	}
	connection, err := startChatServer(os.Args[0], []string{"-test.run=^TestChatMobileServerFixture$", chatMobileFixture}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.close()
	if err := chatLoop(connection, os.Stdin, os.Stdout, "/mobile-workspace", ""); err != nil {
		t.Fatal(err)
	}
}

// The fake server validates actual JSON-RPC replies and emits deterministic
// streaming, question, approval, and interrupt events without running a model.
func TestChatMobileServerFixture(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != chatMobileFixture {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	send := func(value any) {
		if err := encoder.Encode(value); err != nil {
			os.Exit(2)
		}
	}
	for {
		var message appMessage
		if decoder.Decode(&message) != nil {
			os.Exit(0)
		}
		reply := func(value any) { send(map[string]any{"id": message.ID, "result": value}) }
		switch message.Method {
		case "initialize":
			reply(map[string]any{})
		case "initialized":
		case "thread/start":
			reply(map[string]any{"thread": map[string]string{"id": "thread-mobile"}})
		case "turn/start":
			var params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(message.Params, &params) != nil || len(params.Input) != 1 {
				os.Exit(3)
			}
			switch params.Input[0].Text {
			case chatMobilePrompt:
				reply(map[string]any{"turn": map[string]string{"id": "turn-question"}})
				send(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]string{"id": "turn-question"}}})
				send(map[string]any{
					"id": "question-mobile", "method": "item/tool/requestUserInput",
					"params": map[string]any{
						"isBlocking": true, "itemId": "question-item", "threadId": "thread-mobile", "turnId": "turn-question",
						"questions": []any{map[string]any{
							"header": "Style", "id": "style", "question": "Choose a response style",
							"options": []any{map[string]string{"label": "Detailed", "description": "More context"}, map[string]string{"label": "Concise", "description": "Short response"}},
						}},
					},
				})
			case "Need approval":
				reply(map[string]any{"turn": map[string]string{"id": "turn-approval"}})
				send(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]string{"id": "turn-approval"}}})
				send(map[string]any{
					"id": "approval-mobile", "method": "item/commandExecution/requestApproval",
					"params": map[string]string{"itemId": "approval-item", "command": "echo approved"},
				})
			case "Cancel turn":
				reply(map[string]any{"turn": map[string]string{"id": "turn-cancel"}})
				send(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]string{"id": "turn-cancel"}}})
				send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"delta": "CANCEL_READY"}})
			default:
				os.Exit(4)
			}
		case "turn/interrupt":
			reply(map[string]any{})
			send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"delta": "CANCELLED_OK"}})
			send(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]string{"id": "turn-cancel", "status": "interrupted"}}})
		default:
			switch string(message.ID) {
			case `"question-mobile"`:
				var result struct {
					Answers map[string]struct {
						Answers []string `json:"answers"`
					} `json:"answers"`
				}
				if json.Unmarshal(message.Result, &result) != nil || len(result.Answers["style"].Answers) != 1 || result.Answers["style"].Answers[0] != "Concise" {
					os.Exit(5)
				}
				send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"delta": "QUESTION_OK"}})
				time.Sleep(250 * time.Millisecond)
				send(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]string{"id": "turn-question", "status": "completed"}}})
			case `"approval-mobile"`:
				var result struct {
					Decision string `json:"decision"`
				}
				if json.Unmarshal(message.Result, &result) != nil || result.Decision != "accept" {
					os.Exit(6)
				}
				send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"delta": "APPROVED_OK"}})
				send(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]string{"id": "turn-approval", "status": "completed"}}})
			default:
				os.Exit(7)
			}
		}
	}
}

type chatMobilePTY struct {
	terminal *os.File
	command  *exec.Cmd
	output   strings.Builder
	chunks   chan string
	done     chan error
	finished bool
}

func TestChatMobileConversationOn40ColumnPTY(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestChatMobilePTYChild$")
	command.Env = chatMobileTestEnv(map[string]string{
		chatMobileChildEnv: "1", "TERM": "xterm-256color", "NO_COLOR": "1", "TMUX": "", "TMUX_PANE": "", "JARA_CHAT_PLAIN": "1",
	})
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 40, Rows: 20})
	if err != nil {
		t.Fatal(err)
	}
	session := &chatMobilePTY{terminal: terminal, command: command, chunks: make(chan string, 32), done: make(chan error, 1)}
	go session.read()
	go func() { session.done <- command.Wait() }()
	defer session.close(t)

	session.waitCount(t, "You> ", 1)
	session.sendLine(t, "/compose")
	session.waitCount(t, "Compose> ", 1)
	// Send one pasted block; the PTY's canonical line discipline delivers its
	// embedded newlines as separate scanner lines to the composer.
	session.sendPaste(t, "Summarize\nthis pasted block\n/send\n")
	session.wait(t, "Choose a response style")
	session.wait(t, "2) Concise")
	if strings.Contains(session.output.String(), "QUESTION_OK") {
		t.Fatal("server streamed a response before the question was answered")
	}
	session.sendLine(t, "/answer 1 2")
	session.wait(t, "QUESTION_OK")
	session.waitNoMarker(t, "[completed]", 100*time.Millisecond)
	session.waitCount(t, "[completed]", 1)

	session.sendLine(t, "Need approval")
	session.wait(t, "Approval required (1 pending)")
	session.waitNoMarker(t, "APPROVED_OK", 150*time.Millisecond)
	session.sendLine(t, "/approve")
	session.wait(t, "APPROVED_OK")
	session.waitCount(t, "[completed]", 2)

	session.sendLine(t, "Cancel turn")
	session.wait(t, "CANCEL_READY")
	if _, err := session.terminal.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	session.wait(t, "CANCELLED_OK")
	session.wait(t, "[interrupted]")
	session.sendLine(t, "/quit")
	select {
	case err := <-session.done:
		session.finished = true
		if err != nil {
			t.Fatalf("chat child exited with error: %v; transcript=%q", err, session.output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("chat child did not exit: %q", session.output.String())
	}
	if !strings.Contains(session.output.String(), "[interrupted]") || !strings.Contains(session.output.String(), "APPROVED_OK") {
		t.Fatalf("terminal transcript missed expected turn state: %q", session.output.String())
	}
}

func (s *chatMobilePTY) read() {
	defer close(s.chunks)
	buffer := make([]byte, 4096)
	for {
		n, err := s.terminal.Read(buffer)
		if n > 0 {
			s.chunks <- string(buffer[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *chatMobilePTY) wait(t *testing.T, needle string) {
	t.Helper()
	s.waitCount(t, needle, 1)
}

func (s *chatMobilePTY) waitCount(t *testing.T, needle string, count int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for strings.Count(s.output.String(), needle) < count {
		select {
		case chunk, ok := <-s.chunks:
			if !ok {
				t.Fatalf("PTY closed before %q: %s", needle, s.output.String())
			}
			s.output.WriteString(chunk)
		case err := <-s.done:
			s.finished = true
			t.Fatalf("chat child exited before %q: %v; output=%q", needle, err, s.output.String())
		case <-deadline.C:
			t.Fatalf("timed out waiting for %q: %s", needle, s.output.String())
		}
	}
}

func (s *chatMobilePTY) waitNoMarker(t *testing.T, marker string, duration time.Duration) {
	t.Helper()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		select {
		case chunk, ok := <-s.chunks:
			if !ok {
				return
			}
			s.output.WriteString(chunk)
			if strings.Contains(s.output.String(), marker) {
				t.Fatalf("unexpectedly rendered %q: %s", marker, s.output.String())
			}
		case <-deadline.C:
			return
		}
	}
}

func (s *chatMobilePTY) sendLine(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.terminal, line+"\n"); err != nil {
		t.Fatalf("send %q: %v", line, err)
	}
}

func (s *chatMobilePTY) sendPaste(t *testing.T, paste string) {
	t.Helper()
	if _, err := io.WriteString(s.terminal, paste); err != nil {
		t.Fatalf("paste text: %v", err)
	}
}

func (s *chatMobilePTY) close(t *testing.T) {
	t.Helper()
	_ = s.terminal.Close()
	if !s.finished {
		_ = s.command.Process.Kill()
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
			t.Errorf("chat child could not be reaped")
		}
	}
}

func chatMobileTestEnv(overrides map[string]string) []string {
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[key]; !replaced {
			env = append(env, item)
		}
	}
	for key, value := range overrides {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}
	return env
}
