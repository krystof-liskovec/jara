package jara

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regressions for bugs the daemon tests found.

// A session ending (/quit) closes its socket but lives on while it hands the
// workspace over; a window opened then must not wait out the startup timeout.
func TestConnectingToAnEndingSessionReturnsWhenItExits(t *testing.T) {
	useChatDaemonData(t)
	paths := chatDaemonTestPaths(t, "w-1")
	ending := exec.Command("/bin/sh", "-c", "sleep 0.5")
	if err := ending.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = ending.Wait() }() // reaped, so the PID stops existing
	if err := os.WriteFile(paths.pid, []byte(strconv.Itoa(ending.Process.Pid)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := connectExistingChatDaemon("w-1"); !errors.Is(err, errChatDaemonStopped) {
		t.Fatalf("connect to an ending session = %v", err)
	}
	if waited := time.Since(started); waited > 10*time.Second {
		t.Fatalf("waited %s for a session that had exited", waited)
	}
}

func TestHandoffWaitsForTheDaemonGreeting(t *testing.T) {
	useChatDaemonData(t)
	workspace, _ := workspacePath("w-1")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	paths := registerChatDaemon(t, "w-1", chatDaemonState{Workspace: "demo", CWD: workspace, CreatedAt: 1_700_000_000_000_000_000})
	listener, err := net.Listen("unix", paths.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Like attach: greet first, and drop a client that already hung up.
		time.Sleep(50 * time.Millisecond)
		if _, err := conn.Write([]byte(`{"method":"jara/ready","params":{}}` + "\n")); err != nil {
			received <- "greeting failed: " + err.Error()
			return
		}
		line, _ := bufio.NewReader(conn).ReadString('\n')
		received <- strings.TrimSpace(line)
		_ = os.Remove(paths.pid) // the daemon stops after a handoff
	}()
	if _, has, err := prepareChatDaemonForCodex("w-1"); err != nil || !has {
		t.Fatalf("handoff = %v %v", has, err)
	}
	if line := <-received; line != `{"method":"jara/handoff"}` {
		t.Fatalf("daemon received %q", line)
	}
}

func TestResolvedServerRequestsAreNotReplayed(t *testing.T) {
	queue := newChatDaemonRequestQueue()
	hub := &chatDaemonClientHub{clients: map[*chatDaemonClient]struct{}{}, requests: queue}
	hub.broadcast([]byte(`{"id":7,"method":"item/commandExecution/requestApproval"}`), appMessage{ID: json.RawMessage(`7`), Method: "item/commandExecution/requestApproval"})
	hub.broadcast([]byte(`{"id":"q","method":"item/tool/requestUserInput"}`), appMessage{ID: json.RawMessage(`"q"`), Method: "item/tool/requestUserInput"})
	hub.broadcast([]byte(`{"method":"serverRequest/resolved"}`), appMessage{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"t","requestId":7}`)})
	if len(queue.byID) != 1 || len(queue.order) != 1 || queue.order[0] != `"q"` {
		t.Fatalf("queue after resolve: %v %v", queue.byID, queue.order)
	}
	if !queue.claim(json.RawMessage(`"q"`)) || len(queue.order) != 0 {
		t.Fatalf("claimed requests leave the replay order: %v", queue.order)
	}
}

func TestStoppedCrunchStaysStoppedAfterAUsageLimit(t *testing.T) {
	var input bytes.Buffer
	var inputMu sync.Mutex
	idle := func() bool { return false }
	m := newTestCrunchManager(t, &chatCrunchJob{Mode: "prompt", Prompt: "work", Status: "pending", CreatedAt: time.Now()})
	if err := m.tick(&input, &inputMu, idle); err != nil {
		t.Fatal(err)
	}
	start := crunchLines(t, &input)
	if err := m.stop(&input, &inputMu, "th"); err != nil {
		t.Fatal(err)
	}
	m.handleAppMessage(appResponse(t, start[0]["id"].(string), nil, "You've hit your usage limit"), &input, &inputMu, idle)
	if current := m.current(); current.Status != "stopped" {
		t.Fatalf("a stopped crunch came back as %q", current.Status)
	}
	if lines := crunchLines(t, &input); len(lines) != 0 {
		t.Fatalf("a stopped crunch sent %v", lines)
	}
	if saved := savedCrunchJob(t, m); saved.Status != "stopped" {
		t.Fatalf("saved status %q", saved.Status)
	}
}

func TestStartedDaemonIsReapedWhenItExits(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs /bin/sh")
	}
	t.Setenv("JARA_DATA", shortTempDir(t))
	// The daemon binary is this test binary, which TestMain turns into a fake
	// app-server that exits as soon as its input ends: a crash at startup.
	t.Setenv("JARA_FAKE_AGENT", "1")
	pid, err := startChatDaemonProcess(Request{ID: "w-1"}, filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Skipf("cannot start a daemon process here: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for chatProcessExists(pid) {
		if time.Now().After(deadline) {
			t.Fatal("an exited daemon still looks alive (unreaped zombie)")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
