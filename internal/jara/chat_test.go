package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Subprocess fixture exercises actual pipes and bidirectional protocol traffic.
func TestChatAppServerFixture(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "jara-chat-fixture" {
		return
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			os.Exit(2)
		}
	}
	for {
		var m appMessage
		if dec.Decode(&m) != nil {
			os.Exit(0)
		}
		reply := func(result any) { send(map[string]any{"id": m.ID, "result": result}) }
		switch m.Method {
		case "initialize":
			reply(map[string]any{})
		case "initialized":
		case "thread/start", "thread/resume":
			reply(map[string]any{"thread": map[string]any{"id": "thread-fixture"}})
		case "turn/start":
			reply(map[string]any{"turn": map[string]string{"id": "turn-fixture"}})
			send(map[string]any{"id": "approval-fixture", "method": "item/commandExecution/requestApproval", "params": map[string]string{"itemId": "cmd", "command": "echo fixture"}})
		case "turn/interrupt":
			reply(map[string]any{})
			send(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]string{"id": "turn-fixture", "status": "interrupted"}}})
		default:
			if string(m.ID) == `"approval-fixture"` {
				var result struct{ Decision string }
				_ = json.Unmarshal(m.Result, &result)
				if result.Decision == "cancel" {
					continue
				}
				if result.Decision != "decline" && result.Decision != "accept" {
					os.Exit(3)
				}
				send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"delta": "Denied safely\x1b[31m"}})
				send(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]string{"status": "completed"}}})
			} else {
				os.Exit(4)
			}
		}
	}
}

type chatTestOutput struct{ writes chan string }

func (w chatTestOutput) Write(b []byte) (int, error) { w.writes <- string(b); return len(b), nil }
func TestChatProtocolConversation(t *testing.T) {
	c, err := startChatServer(os.Args[0], []string{"-test.run=^TestChatAppServerFixture$", "jara-chat-fixture"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	out := chatTestOutput{make(chan string, 64)}
	done := make(chan error, 1)
	go func() { done <- chatLoop(c, reader, out, "/workspace", "") }()
	var transcript strings.Builder
	wait := func(needle string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for !strings.Contains(transcript.String(), needle) {
			select {
			case s := <-out.writes:
				transcript.WriteString(s)
			case err := <-done:
				t.Fatalf("chat ended early: %v", err)
			case <-timeout:
				t.Fatalf("missing %q in %q", needle, transcript.String())
			}
		}
	}
	send := func(s string) {
		t.Helper()
		if _, err := fmt.Fprintln(writer, s); err != nil {
			t.Fatal(err)
		}
	}
	wait("You> ")
	send("Hello")
	wait("Type /approve")
	send("/deny")
	wait("[completed]")
	if strings.Contains(transcript.String(), "\x1b") {
		t.Fatal("untrusted terminal escape rendered")
	}
	transcript.Reset()
	send("/resume saved-thread")
	wait("Conversation thread-fixture\n\nYou> ")
	transcript.Reset()
	send("Cancel this turn")
	wait("Type /approve")
	send("/cancel")
	wait("[interrupted]")
	send("/quit")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit")
	}
}
func TestChatOptionsRejectUnsupportedArguments(t *testing.T) {
	if _, err := chatServerArgs([]string{"--unsupported-jara-option"}); err == nil {
		t.Fatal("unsupported argument accepted")
	}
	got, err := chatServerArgs([]string{"--model", "example", "-c", "sandbox_mode=\"read-only\""})
	if err != nil || !bytes.Contains([]byte(strings.Join(got, " ")), []byte(`model="example"`)) {
		t.Fatalf("args: %v, %v", got, err)
	}
}

func TestChatUnsupportedRequestPreservesID(t *testing.T) {
	var wire, output bytes.Buffer
	c := &appConnection{encoder: json.NewEncoder(&wire)}
	u := &chatUI{c: c, out: &output}
	if err := u.event(appMessage{ID: json.RawMessage(`"question-1"`), Method: "mcpServer/elicitation/request", Params: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID    string
		Error struct{ Code int }
	}
	if err := json.Unmarshal(wire.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "question-1" || response.Error.Code != -32601 {
		t.Fatalf("unexpected response: %s", wire.String())
	}
}

func TestChatFullAccessArguments(t *testing.T) {
	for _, flag := range []string{"--dangerously-bypass-approvals-and-sandbox", "--yolo"} {
		t.Run(flag, func(t *testing.T) {
			got, err := chatServerArgs([]string{flag, "-c", `model_reasoning_effort="high"`})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"app-server", "--listen", "stdio://", "-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`, "-c", `model_reasoning_effort="high"`}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
