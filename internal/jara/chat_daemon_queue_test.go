package jara

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeDaemonClient returns a daemon-side client and the UI end of its socket.
func pipeDaemonClient(t *testing.T) (*chatDaemonClient, net.Conn, *bufio.Reader) {
	t.Helper()
	daemonSide, uiSide := net.Pipe()
	t.Cleanup(func() { _ = daemonSide.Close(); _ = uiSide.Close() })
	return &chatDaemonClient{conn: daemonSide}, uiSide, bufio.NewReader(uiSide)
}

func readPipeLines(t *testing.T, reader *bufio.Reader, count int) []string {
	t.Helper()
	lines := make(chan []string, 1)
	go func() {
		var out []string
		for len(out) < count {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			out = append(out, strings.TrimSuffix(line, "\n"))
		}
		lines <- out
	}()
	select {
	case out := <-lines:
		if len(out) != count {
			t.Fatalf("read %d lines, want %d: %q", len(out), count, out)
		}
		return out
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out reading %d lines", count)
		return nil
	}
}

func TestChatDaemonRequestQueueReplaysUnclaimedRequestsInOrder(t *testing.T) {
	queue := newChatDaemonRequestQueue()
	queue.add(json.RawMessage(`"b"`), []byte(`{"id":"b","method":"second"}`))
	queue.add(json.RawMessage(`1`), []byte(`{"id":1,"method":"first-old"}`))
	queue.add(json.RawMessage(`"c"`), []byte(`{"id":"c","method":"third"}`))
	// Re-adding an ID replaces the line but keeps its original position.
	queue.add(json.RawMessage(`1`), []byte(`{"id":1,"method":"first"}`))

	if !queue.claim(json.RawMessage(`"c"`)) {
		t.Fatal("pending request could not be claimed")
	}
	if queue.claim(json.RawMessage(`"c"`)) || queue.claim(json.RawMessage(`"missing"`)) {
		t.Fatal("a request was claimed twice or without being queued")
	}
	// IDs are compared verbatim: the string "1" is not the number 1.
	if queue.claim(json.RawMessage(`"1"`)) {
		t.Fatal("string ID claimed a numeric request")
	}
	queue.remove(json.RawMessage(`"b"`))
	queue.remove(json.RawMessage(`"never"`))
	// A claimed ID can be queued again and is replayed once.
	queue.add(json.RawMessage(`"c"`), []byte(`{"id":"c","method":"again"}`))

	client, _, reader := pipeDaemonClient(t)
	done := make(chan error, 1)
	go func() { done <- queue.writePending(client) }()
	lines := readPipeLines(t, reader, 2)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if lines[0] != `{"id":1,"method":"first"}` || lines[1] != `{"id":"c","method":"again"}` {
		t.Fatalf("replayed %q", lines)
	}
}

func TestChatDaemonRequestQueueWriteFailure(t *testing.T) {
	queue := newChatDaemonRequestQueue()
	queue.add(json.RawMessage(`1`), []byte(`{"id":1,"method":"x"}`))
	client, ui, _ := pipeDaemonClient(t)
	_ = ui.Close()
	if err := queue.writePending(client); err == nil {
		t.Fatal("write to a closed client succeeded")
	}
}

func TestChatDaemonClientRouterRestoresOriginalIDs(t *testing.T) {
	router := newChatDaemonClientRouter()
	first, second := &chatDaemonClient{}, &chatDaemonClient{}
	line1, virtual1, err := router.rewrite(first, []byte(`{"id":"same","method":"a","params":{"x":1}}`), json.RawMessage(`"same"`))
	if err != nil {
		t.Fatal(err)
	}
	line2, virtual2, err := router.rewrite(second, []byte(`{"id":"same","method":"b"}`), json.RawMessage(`"same"`))
	if err != nil {
		t.Fatal(err)
	}
	if string(virtual1) == string(virtual2) || string(virtual1) != `"jara-client-1"` {
		t.Fatalf("virtual IDs %s %s", virtual1, virtual2)
	}
	if string(line1) != `{"id":"jara-client-1","method":"a","params":{"x":1}}` || !strings.Contains(string(line2), `"jara-client-2"`) {
		t.Fatalf("rewritten lines %s %s", line1, line2)
	}

	client, response, ok := router.response([]byte(`{"id":"jara-client-2","result":{"ok":true}}`), virtual2)
	if !ok || client != second || string(response) != `{"id":"same","result":{"ok":true}}` {
		t.Fatalf("response = %v %s %v", client == second, response, ok)
	}
	if _, _, ok = router.response([]byte(`{"id":"jara-client-2"}`), virtual2); ok {
		t.Fatal("a route was used twice")
	}
	// An undecodable response still identifies the client but has no body.
	client, response, ok = router.response([]byte(`not json`), virtual1)
	if !ok || client != first || response != nil {
		t.Fatalf("invalid response = %v %q %v", client == first, response, ok)
	}

	if _, _, err = router.rewrite(first, []byte(`{broken`), json.RawMessage(`1`)); err == nil {
		t.Fatal("invalid JSON was rewritten")
	}
	_, virtual3, _ := router.rewrite(first, []byte(`{"id":3,"method":"c"}`), json.RawMessage(`3`))
	router.discard(virtual3)
	if _, _, ok = router.response([]byte(`{"id":"jara-client-4"}`), virtual3); ok {
		t.Fatal("discarded route still answered")
	}
}

func TestChatDaemonClientHubBroadcastQueuesServerRequestsAndDropsDeadClients(t *testing.T) {
	queue := newChatDaemonRequestQueue()
	hub := newChatDaemonClientHub(queue)
	liveDaemon, liveUI := net.Pipe()
	deadDaemon, deadUI := net.Pipe()
	defer liveUI.Close()
	ready := remoteChatServerReady{CWD: "/w", Workspace: "demo"}
	attached := make(chan error, 2)
	liveReader := bufio.NewReader(liveUI)
	go func() { _, err := hub.attach(liveDaemon, ready); attached <- err }()
	if line := readPipeLines(t, liveReader, 1)[0]; line != `{"method":"jara/ready","params":{"cwd":"/w","workspace":"demo"}}` {
		t.Fatalf("ready line %q", line)
	}
	if err := <-attached; err != nil {
		t.Fatal(err)
	}
	go func() { _, err := hub.attach(deadDaemon, ready); attached <- err }()
	readPipeLines(t, bufio.NewReader(deadUI), 1)
	if err := <-attached; err != nil {
		t.Fatal(err)
	}
	if hub.count() != 2 {
		t.Fatalf("count = %d", hub.count())
	}
	_ = deadUI.Close()

	request := []byte(`{"id":"ap","method":"approve"}`)
	var message appMessage
	_ = json.Unmarshal(request, &message)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); hub.broadcast(request, message) }()
	if line := readPipeLines(t, liveReader, 1)[0]; line != string(request) {
		t.Fatalf("broadcast %q", line)
	}
	wg.Wait()
	if hub.count() != 1 {
		t.Fatalf("dead client was kept: %d", hub.count())
	}
	if !queue.claim(json.RawMessage(`"ap"`)) {
		t.Fatal("server request was not queued for replay")
	}

	// Attaching to a closed connection fails without registering it.
	closedDaemon, closedUI := net.Pipe()
	_ = closedUI.Close()
	if _, err := hub.attach(closedDaemon, ready); err == nil || hub.count() != 1 {
		t.Fatalf("attach to closed client = %v, count %d", err, hub.count())
	}
	hub.closeAll()
	if hub.count() != 0 {
		t.Fatal("closeAll kept clients")
	}
	if _, err := liveUI.Read(make([]byte, 1)); err == nil {
		t.Fatal("closeAll left the client connection open")
	}
}

func TestWriteChatDaemonLineAppendsNewline(t *testing.T) {
	client, _, reader := pipeDaemonClient(t)
	done := make(chan error, 1)
	go func() { done <- client.write([]byte(`{"a":1}`)) }()
	if line := readPipeLines(t, reader, 1)[0]; line != `{"a":1}` {
		t.Fatalf("line %q", line)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	daemonSide, uiSide := net.Pipe()
	_ = uiSide.Close()
	if err := writeChatDaemonLine(daemonSide, []byte("x")); err == nil {
		t.Fatal("write to closed pipe succeeded")
	}
}

func TestApplyChatDaemonEventTracksTurnsAndThreads(t *testing.T) {
	var stateMu, pendingMu sync.Mutex
	state := chatDaemonState{Conversation: "old", ThreadLoaded: true}
	pending := map[string]string{`"t"`: "thread/start", `"r"`: "thread/resume", `"i"`: "initialize", `"u"`: "turn/start", `"x"`: "model/list"}
	apply := func(line string) bool {
		return applyChatDaemonEvent([]byte(line), &state, &stateMu, &pending, &pendingMu)
	}
	if apply(`not json`) || apply(`{"method":"item/started"}`) {
		t.Fatal("irrelevant lines changed state")
	}
	if !apply(`{"method":"turn/started"}`) || !state.ActiveTurn {
		t.Fatal("turn/started did not mark the turn active")
	}
	if !apply(`{"method":"turn/completed"}`) || state.ActiveTurn {
		t.Fatal("turn/completed did not clear the turn")
	}
	state.ActiveTurn = true
	if !apply(`{"id":"u","error":{"message":"no"}}`) || state.ActiveTurn {
		t.Fatal("rejected turn/start left the turn active")
	}
	state.ThreadLoaded = false
	if !apply(`{"id":"r","error":{"message":"missing"}}`) || !state.ThreadLoaded || state.Conversation != "old" {
		t.Fatalf("failed resume = %+v", state)
	}
	if !apply(`{"id":"t","result":{"thread":{"id":"new"}}}`) || state.Conversation != "new" {
		t.Fatalf("thread/start = %+v", state)
	}
	if apply(`{"id":"i","error":{"message":"bad handshake"}}`) || state.Initialized {
		t.Fatal("unrelated initialize error marked the session initialized")
	}
	pending[`"i"`] = "initialize"
	if !apply(`{"id":"i","error":{"message":"Already Initialized"}}`) || !state.Initialized {
		t.Fatal("already-initialized error was not recognized")
	}
	if apply(`{"id":"x","result":{}}`) || len(pending) != 0 {
		t.Fatalf("pending entries were not consumed: %v", pending)
	}
	state.Conversation, state.ThreadLoaded = "", false
	pending[`"r"`] = "thread/resume"
	if !apply(`{"id":"r","error":{"message":"missing"}}`) || state.ThreadLoaded {
		t.Fatal("failed resume without a conversation claimed a loaded thread")
	}
}
