package jara

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestDecodeRemoteChatReady(t *testing.T) {
	for _, test := range []struct {
		message appMessage
		want    remoteChatServerReady
		err     string
	}{
		{appMessage{Method: "jara/ready", Params: json.RawMessage(`{"cwd":"/w","workspace":"demo","conversation":"th","initialized":true,"active_turn":true}`)}, remoteChatServerReady{CWD: "/w", Workspace: "demo", Conversation: "th", Initialized: true, ActiveTurn: true}, ""},
		{appMessage{Method: "jara/ready", Params: json.RawMessage(`{"managed_session":true}`)}, remoteChatServerReady{ManagedSession: true}, ""},
		{appMessage{Method: "jara/ready", Params: json.RawMessage(`[]`)}, remoteChatServerReady{}, "decode remote app-server metadata"},
		{appMessage{Method: "jara/error", Params: json.RawMessage(`{"message":"locked \u001b[31m"}`)}, remoteChatServerReady{}, "remote Jara session: locked"},
		{appMessage{Method: "jara/error", Params: json.RawMessage(`"x"`)}, remoteChatServerReady{}, "decode remote Jara session error"},
		{appMessage{Method: "thread/started"}, remoteChatServerReady{}, "did not provide workspace metadata"},
	} {
		ready, err := decodeRemoteChatReady(test.message)
		if test.err != "" {
			if err == nil || !strings.Contains(err.Error(), test.err) || strings.Contains(err.Error(), "\x1b") {
				t.Fatalf("%s: err = %v, want %q", test.message.Params, err, test.err)
			}
			continue
		}
		if err != nil || ready != test.want {
			t.Fatalf("%s: ready = %+v %v", test.message.Params, ready, err)
		}
	}
}

// remoteChatPeer serves the given lines to a UI connection, then hangs up.
func remoteChatPeer(t *testing.T, lines ...string) net.Conn {
	t.Helper()
	ui, daemon := net.Pipe()
	go func() {
		defer daemon.Close()
		for _, line := range lines {
			if _, err := daemon.Write([]byte(line + "\n")); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = ui.Close() })
	return ui
}

func TestAwaitRemoteChatReady(t *testing.T) {
	app := startChatConnection(remoteChatPeer(t, `{"method":"jara/ready","params":{"cwd":"/w"}}`))
	defer app.close()
	if ready, err := awaitRemoteChatReady(app); err != nil || ready.CWD != "/w" {
		t.Fatalf("ready = %+v %v", ready, err)
	}

	hungUp := startChatConnection(remoteChatPeer(t))
	defer hungUp.close()
	if _, err := awaitRemoteChatReady(hungUp); err == nil || !strings.Contains(err.Error(), "startup ended: EOF") {
		t.Fatalf("hang-up = %v", err)
	}

	garbled := startChatConnection(remoteChatPeer(t, `not json`))
	defer garbled.close()
	if _, err := awaitRemoteChatReady(garbled); err == nil || !strings.Contains(err.Error(), "invalid app-server message") {
		t.Fatalf("garbled = %v", err)
	}
}

func TestRunRemoteChatClientConnectionRejectsBadHandshake(t *testing.T) {
	for _, test := range []struct {
		line, want string
	}{
		{`{"method":"jara/ready","params":{"workspace":"demo"}}`, "returned no workspace path"},
		{`{"method":"jara/error","params":{"message":"workspace is locked"}}`, "remote Jara session: workspace is locked"},
		{`{"method":"turn/started"}`, "did not provide workspace metadata"},
	} {
		err := runRemoteChatClientConnection(Request{ID: "w-1"}, remoteChatPeer(t, test.line))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: got %v, want %q", test.line, err, test.want)
		}
	}
}

func TestRunRemoteChatClientRequiresRunningDaemon(t *testing.T) {
	useChatDaemonData(t)
	if err := runRemoteChatClient(Request{ID: "w-1", Worktree: true}); err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("worktree = %v", err)
	}
	if err := runRemoteChatClient(Request{ID: "w-1"}); err == nil || !strings.Contains(err.Error(), "no persistent Jara session is running") {
		t.Fatalf("missing daemon = %v", err)
	}
	if err := runRemoteChatClient(Request{ID: "bad/id"}); err == nil {
		t.Fatal("invalid ID accepted")
	}
}
