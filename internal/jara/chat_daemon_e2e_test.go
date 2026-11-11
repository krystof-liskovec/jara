package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentfake"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

// createChatDaemonWorkspace writes workspace metadata below the data root.
func createChatDaemonWorkspace(t *testing.T, id string, ready bool) string {
	t.Helper()
	path, err := workspacePath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = fsutil.WriteJSON(path+".json", Workspace{ID: id, Name: "demo", Path: path, Created: "2026-01-01T00:00:00Z", Ready: ready}); err != nil {
		t.Fatal(err)
	}
	return path
}

// useFakeDaemonTools replaces PATH with stub tmux and mise commands. The stub
// mise runs the test binary, which TestMain turns into a fake Codex
// app-server because JARA_FAKE_AGENT is set.
func useFakeDaemonTools(t *testing.T, tmuxOutput string) {
	t.Helper()
	bin := t.TempDir()
	scripts := map[string]string{
		"tmux": "#!/bin/sh\nprintf '%s' '" + tmuxOutput + "'\n",
		"mise": "#!/bin/sh\ncase \"$1\" in\ninstall) exit 0 ;;\nexec) shift; [ \"$1\" = -- ] && shift; exec \"$@\" ;;\nesac\nexit 1\n",
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("JARA_FAKE_AGENT", "1")
	t.Setenv("JARA_FAKE_LOG", filepath.Join(t.TempDir(), "fake.jsonl"))
	t.Setenv("JARA_CODEX_ARGS", "[]")
	t.Setenv("HOME", t.TempDir())
}

// startPersistentChatDaemon runs runChatDaemon in-process, registered under the
// test's own PID as the launcher would do, and waits for its socket.
func startPersistentChatDaemon(t *testing.T, request Request) (chatDaemonPaths, chan error) {
	t.Helper()
	paths := chatDaemonTestPaths(t, request.ID)
	if err := os.WriteFile(paths.pid, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runChatDaemon(request) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("daemon stopped during startup: %v", err)
		default:
		}
		if conn, err := net.Dial("unix", paths.socket); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = sendChatDaemonControl(paths.socket, "jara/stop")
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("daemon did not stop during cleanup")
			}
		}
	})
	return paths, done
}

// sendChatDaemonControl waits for jara/ready before sending a control
// message, so the daemon has registered the client and will read it.
func sendChatDaemonControl(socket, method string) error {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = bufio.NewReader(conn).ReadString('\n'); err != nil {
		return err
	}
	if err = json.NewEncoder(conn).Encode(map[string]string{"method": method}); err != nil {
		return err
	}
	_, err = conn.Read(make([]byte, 1)) // returns once the daemon closes the client
	if err != nil && err.Error() != "EOF" {
		return err
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func waitDaemonExit(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		done <- err // keep the result for cleanup
		if err != nil {
			t.Fatalf("daemon returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit")
	}
}

// swapStdio redirects os.Stdin and os.Stdout to pipes until cleanup.
func swapStdio(t *testing.T) (*os.File, *bufio.Reader) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = stdin, stdout
		_ = inW.Close()
		_ = outW.Close()
		_ = inR.Close()
		_ = outR.Close()
	})
	return inW, bufio.NewReader(outR)
}

func readJSONLine(t *testing.T, reader *bufio.Reader) map[string]any {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		lines <- line
	}()
	select {
	case line := <-lines:
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("invalid line %q: %v", line, err)
		}
		return message
	case <-time.After(5 * time.Second):
		t.Fatal("no output line")
		return nil
	}
}

func TestPersistentChatDaemonProxiesRemoteServerAndHandsOff(t *testing.T) {
	useChatDaemonData(t)
	useFakeDaemonTools(t, "")
	workspace := createChatDaemonWorkspace(t, "w-e2e", true)
	stdin, stdout := swapStdio(t) // before the daemon goroutine reads os.Stdin
	paths, done := startPersistentChatDaemon(t, Request{UserInterface: "jara", ID: "w-e2e"})
	if info, err := os.Stat(paths.socket); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket = %v %v", info, err)
	}
	if lock, _ := workspacePath("w-e2e"); !fileExists(filepath.Join(lock+".lock", "chat-daemon.pid")) {
		t.Fatal("daemon did not mark its workspace lock")
	}

	proxied := make(chan error, 1)
	go func() { proxied <- runRemoteChatServer(Request{UserInterface: "jara", ID: "w-e2e"}) }()
	ready := readJSONLine(t, stdout)
	if ready["method"] != "jara/ready" || ready["params"].(map[string]any)["cwd"] != workspace {
		t.Fatalf("ready = %v", ready)
	}
	send := func(message any) {
		data, _ := json.Marshal(message)
		if _, err := stdin.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{}})
	if response := readJSONLine(t, stdout); response["id"] != float64(1) {
		t.Fatalf("initialize = %v", response)
	}
	send(map[string]any{"method": "initialized"})
	send(map[string]any{"id": 2, "method": "thread/start", "params": map[string]any{"cwd": workspace}})
	if response := readJSONLine(t, stdout); jsonString(t, response["result"].(map[string]any)["thread"].(map[string]any)["id"]) != `"codex-thread-1"` {
		t.Fatalf("thread/start = %v", response)
	}
	send(map[string]any{"id": 3, "method": "turn/start", "params": map[string]any{"threadId": "codex-thread-1", "input": []any{map[string]any{"type": "text", "text": "hi"}}}})
	for _, want := range []string{"", "turn/started", "item/completed", "turn/completed"} {
		if message := readJSONLine(t, stdout); message["method"] != nil && message["method"] != want || message["method"] == nil && want != "" {
			t.Fatalf("turn stream got %v, want %q", message, want)
		}
	}
	_ = stdin.Close()
	select {
	case err := <-proxied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not finish after stdin closed")
	}

	state, ok := readChatDaemonState("w-e2e")
	if !ok || state.Conversation != "codex-thread-1" || !state.Initialized || state.ActiveTurn || state.Workspace != "demo" || state.CWD != workspace {
		t.Fatalf("daemon state = %+v %v", state, ok)
	}
	if turns := agentfake.ReadLog(t, "codex-turn"); len(turns) != 1 || turns[0] != "codex-thread-1|hi" {
		t.Fatalf("app-server turns = %v", turns)
	}
	// Wait for the proxy's disconnect to be recorded before handing off.
	deadline := time.Now().Add(3 * time.Second)
	for state.Attached && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		state, _ = readChatDaemonState("w-e2e")
	}

	// prepareChatDaemonForCodex sends jara/handoff without reading jara/ready,
	// which loses the request (see the handoff tests); drive the same handoff
	// as a registered client instead.
	if err := sendChatDaemonControl(paths.socket, "jara/handoff"); err != nil {
		t.Fatal(err)
	}
	waitDaemonExit(t, done)
	if chatDaemonRunning("w-e2e") || fileExists(paths.socket) {
		t.Fatal("daemon left its pid file or socket behind")
	}
	if lock, _ := workspacePath("w-e2e"); fileExists(lock + ".lock") {
		t.Fatal("daemon left its workspace lock behind")
	}
	saved, has, err := prepareChatDaemonForCodex("w-e2e")
	if err != nil || !has || saved.Conversation != "codex-thread-1" {
		t.Fatalf("handoff after stop = %+v %v %v", saved, has, err)
	}
}

func TestPersistentChatDaemonRunsRemoteChatClient(t *testing.T) {
	useChatDaemonData(t)
	useFakeDaemonTools(t, "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	createChatDaemonWorkspace(t, "w-ui", true)
	stdin, stdout := swapStdio(t)
	_, done := startPersistentChatDaemon(t, Request{UserInterface: "jara", ID: "w-ui", Conversation: "saved-thread"})
	finished := make(chan error, 1)
	go func() { finished <- runRemoteChatClient(Request{UserInterface: "jara", ID: "w-ui"}) }()
	var transcript strings.Builder
	output := make(chan string, 64)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := stdout.Read(buffer)
			if n > 0 {
				output <- string(buffer[:n])
			}
			if err != nil {
				close(output)
				return
			}
		}
	}()
	wait := func(needle string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for !strings.Contains(transcript.String(), needle) {
			select {
			case chunk, ok := <-output:
				if !ok {
					t.Fatalf("output closed before %q: %q", needle, transcript.String())
				}
				transcript.WriteString(chunk)
			case err := <-finished:
				t.Fatalf("chat ended early (%v): %q", err, transcript.String())
			case <-timeout:
				t.Fatalf("missing %q in %q", needle, transcript.String())
			}
		}
	}
	wait("You> ")
	fmt.Fprintln(stdin, "hello daemon")
	wait("[completed]")
	fmt.Fprintln(stdin, "/quit")
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat client did not exit")
	}
	waitDaemonExit(t, done)
	if turns := agentfake.ReadLog(t, "codex-turn"); len(turns) != 1 || turns[0] != "saved-thread|hello daemon" {
		t.Fatalf("app-server turns = %v", turns)
	}
}
