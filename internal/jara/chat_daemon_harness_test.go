package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeAppServer stands in for the Codex app-server (or agent router) that a
// chat daemon owns. It runs in-process over pipes so tests can script
// server-initiated requests and observe exactly what the daemon forwards.
type fakeAppServer struct {
	t        *testing.T
	in       *io.PipeReader
	out      *io.PipeWriter
	writeMu  sync.Mutex
	handleMu sync.Mutex
	received chan map[string]any
	closed   chan struct{}
	turns    int
	// handle may answer a request; it runs on the reader goroutine.
	handle func(f *fakeAppServer, message map[string]any)
}

func newFakeAppServer(t *testing.T) (*fakeAppServer, io.WriteCloser, io.Reader) {
	daemonOutput, serverOutput := io.Pipe()
	serverInput, daemonInput := io.Pipe()
	f := &fakeAppServer{t: t, in: serverInput, out: serverOutput, received: make(chan map[string]any, 256), closed: make(chan struct{}), handle: defaultFakeAppServerHandler}
	go func() {
		defer close(f.closed)
		defer serverOutput.Close()
		scanner := bufio.NewScanner(serverInput)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			var message map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				continue
			}
			f.received <- message
			f.handleMu.Lock()
			handle := f.handle
			f.handleMu.Unlock()
			if handle != nil {
				handle(f, message)
			}
		}
	}()
	return f, daemonInput, daemonOutput
}

func defaultFakeAppServerHandler(f *fakeAppServer, message map[string]any) {
	id, hasID := message["id"]
	method, _ := message["method"].(string)
	if !hasID || method == "" {
		return
	}
	params, _ := message["params"].(map[string]any)
	switch method {
	case "initialize":
		f.emit(map[string]any{"id": id, "result": map[string]any{}})
	case "thread/start":
		f.emit(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-1"}}})
	case "thread/resume":
		f.emit(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": params["threadId"]}}})
	case "turn/start":
		f.turns++
		turn := fmt.Sprintf("turn-%d", f.turns)
		f.emit(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": turn}}})
		f.emit(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": params["threadId"], "turn": map[string]any{"id": turn}}})
	default:
		f.emit(map[string]any{"id": id, "result": map[string]any{"echo": method}})
	}
}

// setHandler replaces the scripted responder; nil makes the test answer.
func (f *fakeAppServer) setHandler(handle func(f *fakeAppServer, message map[string]any)) {
	f.handleMu.Lock()
	f.handle = handle
	f.handleMu.Unlock()
}

func (f *fakeAppServer) emit(message any) {
	data, err := json.Marshal(message)
	if err != nil {
		f.t.Errorf("encode fake app-server message: %v", err)
		return
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_, _ = f.out.Write(append(data, '\n'))
}

// next returns the next line the daemon forwarded to the app-server.
func (f *fakeAppServer) next() map[string]any {
	f.t.Helper()
	select {
	case message := <-f.received:
		return message
	case <-time.After(3 * time.Second):
		f.t.Fatal("app-server received nothing")
		return nil
	}
}

func (f *fakeAppServer) nextMethod(method string) map[string]any {
	f.t.Helper()
	for {
		if message := f.next(); message["method"] == method {
			return message
		}
	}
}

type daemonHarness struct {
	t         *testing.T
	server    *fakeAppServer
	paths     chatDaemonPaths
	listener  net.Listener
	schedules *chatScheduleManager
	crunch    *chatCrunchManager
	done      chan error
}

// shortTempDir keeps unix socket paths below the platform limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testChatDaemonPaths(dir, id string) chatDaemonPaths {
	base := filepath.Join(dir, id)
	return chatDaemonPaths{socket: base + ".sock", state: base + ".json", schedules: base + ".schedules.json", crunch: base + ".crunch.json", pid: base + ".pid", start: base + ".start", log: base + ".log"}
}

// exitedServerProcess returns a started process with the given exit status
// that serveChatDaemon can wait for; the pipes carry the actual protocol.
func exitedServerProcess(t *testing.T, status int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", status))
	cmd.Env = []string{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

type daemonHarnessOptions struct {
	state      chatDaemonState
	exitStatus int
	setup      func(h *daemonHarness)
	// dir places the daemon files there instead of a fresh temporary directory.
	dir string
	// hooks connects the daemon to a runner, as a session on a device has.
	hooks chatDaemonRunnerHooks
}

func startDaemonHarness(t *testing.T, options daemonHarnessOptions) *daemonHarness {
	t.Helper()
	dir := options.dir
	if dir == "" {
		dir = shortTempDir(t)
	}
	h := &daemonHarness{t: t, paths: testChatDaemonPaths(dir, "w-test"), done: make(chan error, 1)}
	var err error
	if h.schedules, err = newChatScheduleManager(h.paths.schedules); err != nil {
		t.Fatal(err)
	}
	if h.crunch, err = newChatCrunchManager(h.paths.crunch); err != nil {
		t.Fatal(err)
	}
	if h.listener, err = net.Listen("unix", h.paths.socket); err != nil {
		t.Fatal(err)
	}
	server, input, output := newFakeAppServer(t)
	h.server = server
	if options.setup != nil {
		options.setup(h)
	}
	state := options.state
	if state.WorkspaceID == "" {
		state.WorkspaceID = "w-test"
	}
	if state.CWD == "" {
		state.CWD = dir
	}
	process := exitedServerProcess(t, options.exitStatus)
	go func() {
		h.done <- serveChatDaemon(h.listener, process, input, output, h.paths, h.schedules, h.crunch, &state, options.hooks)
	}()
	t.Cleanup(func() {
		select {
		case <-h.done:
		default:
			_ = h.listener.Close()
			select {
			case <-h.done:
			case <-time.After(5 * time.Second):
				t.Error("chat daemon did not stop during cleanup")
			}
		}
	})
	return h
}

func (h *daemonHarness) wait() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		h.done <- err
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("chat daemon did not stop")
		return nil
	}
}

func (h *daemonHarness) assertRunning() {
	h.t.Helper()
	select {
	case err := <-h.done:
		h.done <- err
		h.t.Fatalf("chat daemon stopped unexpectedly: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func (h *daemonHarness) state() chatDaemonState {
	h.t.Helper()
	var state chatDaemonState
	data, err := os.ReadFile(h.paths.state)
	if err == nil {
		err = json.Unmarshal(data, &state)
	}
	if err != nil {
		h.t.Fatalf("read daemon state: %v", err)
	}
	return state
}

func (h *daemonHarness) waitState(description string, ok func(chatDaemonState) bool) chatDaemonState {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var state chatDaemonState
		data, err := os.ReadFile(h.paths.state)
		if err == nil && json.Unmarshal(data, &state) == nil && ok(state) {
			return state
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("daemon state never became %s: %+v", description, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type daemonTestClient struct {
	t     *testing.T
	conn  net.Conn
	lines chan map[string]any
}

func dialDaemonTestClient(t *testing.T, socket string) *daemonTestClient {
	t.Helper()
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c := &daemonTestClient{t: t, conn: conn, lines: make(chan map[string]any, 256)}
	go func() {
		defer close(c.lines)
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			var message map[string]any
			if json.Unmarshal(scanner.Bytes(), &message) == nil {
				c.lines <- message
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return c
}

// attach connects a UI client and consumes its jara/ready handshake.
func (h *daemonHarness) attach() (*daemonTestClient, remoteChatServerReady) {
	h.t.Helper()
	c := dialDaemonTestClient(h.t, h.paths.socket)
	first := c.next()
	if first["method"] != "jara/ready" {
		h.t.Fatalf("first daemon message = %v, want jara/ready", first)
	}
	var ready remoteChatServerReady
	data, _ := json.Marshal(first["params"])
	if err := json.Unmarshal(data, &ready); err != nil {
		h.t.Fatal(err)
	}
	return c, ready
}

func (c *daemonTestClient) send(message any) {
	c.t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err = c.conn.Write(append(data, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

func (c *daemonTestClient) next() map[string]any {
	c.t.Helper()
	select {
	case message, ok := <-c.lines:
		if !ok {
			c.t.Fatal("daemon closed the client connection")
		}
		return message
	case <-time.After(3 * time.Second):
		c.t.Fatal("client received nothing")
		return nil
	}
}

func (c *daemonTestClient) expectNone(wait time.Duration) {
	c.t.Helper()
	select {
	case message, ok := <-c.lines:
		if ok {
			c.t.Fatalf("unexpected daemon message: %v", message)
		}
	case <-time.After(wait):
	}
}

func (c *daemonTestClient) expectClosed() {
	c.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-c.lines:
			if !ok {
				return
			}
		case <-timeout:
			c.t.Fatal("daemon kept the client connection open")
		}
	}
}

func jsonString(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
