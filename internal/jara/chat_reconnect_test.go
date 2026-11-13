package jara

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// scriptedChatSession plays a persistent session for one UI connection: it
// answers thread/resume and turn/start and reports every request it sees.
type scriptedChatSession struct {
	conn     net.Conn
	requests chan map[string]any
}

func startScriptedChatSession(t *testing.T) (*appConnection, *scriptedChatSession) {
	t.Helper()
	ui, session := net.Pipe()
	s := &scriptedChatSession{conn: session, requests: make(chan map[string]any, 64)}
	go func() {
		scanner := bufio.NewScanner(session)
		encoder := json.NewEncoder(session)
		for scanner.Scan() {
			var message map[string]any
			if json.Unmarshal(scanner.Bytes(), &message) != nil {
				continue
			}
			s.requests <- message
			params, _ := message["params"].(map[string]any)
			switch message["method"] {
			case "thread/resume":
				_ = encoder.Encode(map[string]any{"id": message["id"], "result": map[string]any{"thread": map[string]any{"id": params["threadId"]}}})
			case "turn/start":
				_ = encoder.Encode(map[string]any{"id": message["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
			}
		}
	}()
	t.Cleanup(func() { _ = session.Close() })
	return startChatConnection(ui), s
}

func (s *scriptedChatSession) expect(t *testing.T, method string) map[string]any {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case message := <-s.requests:
			if message["method"] == method {
				params, _ := message["params"].(map[string]any)
				return params
			}
		case <-timeout:
			t.Fatalf("session never received %s", method)
			return nil
		}
	}
}

func (s *scriptedChatSession) send(t *testing.T, message any) {
	t.Helper()
	if err := json.NewEncoder(s.conn).Encode(message); err != nil {
		t.Fatal(err)
	}
}

func TestChatReconnectsAndRunsQueuedPrompt(t *testing.T) {
	first, firstSession := startScriptedChatSession(t)
	second, secondSession := startScriptedChatSession(t)
	reconnectCalls := make(chan string, 1)
	release := make(chan struct{})
	options := chatLoopOptions{
		cwd: "/w", id: "th-1", persistentSession: true, initialized: true,
		reconnect: func(thread string) (*appConnection, remoteChatServerReady, error) {
			reconnectCalls <- thread
			<-release
			return second, remoteChatServerReady{CWD: "/w", Conversation: thread, Initialized: true}, nil
		},
	}
	input, typed := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- chatLoopWithOptions(first, input, io.Discard, options) }()

	firstSession.expect(t, "thread/resume")
	_ = firstSession.conn.Close() // the connection drops, e.g. an update stopped the session
	select {
	case thread := <-reconnectCalls:
		if thread != "th-1" {
			t.Fatalf("reconnected to %q, want th-1", thread)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interface did not reconnect")
	}
	if _, err := io.WriteString(typed, "hello while offline\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // the prompt is queued before the session returns
	close(release)

	if params := secondSession.expect(t, "thread/resume"); params["threadId"] != "th-1" {
		t.Fatalf("resumed %v, want th-1", params["threadId"])
	}
	turn := secondSession.expect(t, "turn/start")
	if text := jsonString(t, turn["input"]); !strings.Contains(text, "hello while offline") {
		t.Fatalf("queued prompt was not sent after reconnecting: %s", text)
	}
	_ = typed.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interface did not exit")
	}
}

func TestChatDoesNotReconnectAfterSessionStop(t *testing.T) {
	first, session := startScriptedChatSession(t)
	reconnected := make(chan struct{}, 1)
	options := chatLoopOptions{
		cwd: "/w", id: "th-1", persistentSession: true, initialized: true,
		reconnect: func(string) (*appConnection, remoteChatServerReady, error) {
			reconnected <- struct{}{}
			return nil, remoteChatServerReady{}, io.EOF
		},
	}
	input, typed := io.Pipe()
	defer typed.Close()
	done := make(chan error, 1)
	go func() { done <- chatLoopWithOptions(first, input, io.Discard, options) }()

	session.expect(t, "thread/resume")
	session.send(t, map[string]any{"method": "jara/session/ended", "params": map[string]bool{"reconnect": false}})
	_ = session.conn.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "disconnected") {
			t.Fatalf("stopped session = %v, want a disconnect error", err)
		}
	case <-reconnected:
		t.Fatal("interface restarted a session another client stopped")
	case <-time.After(5 * time.Second):
		t.Fatal("interface kept running after the session stopped")
	}
}
