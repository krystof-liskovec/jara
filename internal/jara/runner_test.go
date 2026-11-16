package jara

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/portable"
	"krystof.liskovec/jara/internal/protocol"
)

// runnerTestHomes isolates Jara's data and both agents' homes.
func runnerTestHomes(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "jara-runner-") // short: holds Unix sockets
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	t.Setenv("JARA_DATA", filepath.Join(base, "d"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(base, "codex"))
	return base
}

func writeNative(t *testing.T, path, data string) {
	t.Helper()
	if err := portable.WriteFileAtomic(path, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func TestConversationBundleMovesBothAgentsBetweenCheckouts(t *testing.T) {
	runnerTestHomes(t)
	from, to := "/home/agent/workspaces/ws1", "/Users/me/src/project"
	codexID, claudeID := "0199aaaa-bbbb-7ccc-8ddd-eeeeffff0001", "5f0c1a6e-3d2b-4c1a-9e8f-000000000002"
	thread := &agentrouter.Thread{ID: codexID, CWD: from, Turns: []agentrouter.TranscriptTurn{{User: "edit " + from + "/main.go", Agent: protocol.AgentCodex}},
		Segments: []agentrouter.ThreadSegment{{Agent: protocol.AgentCodex, BackendID: codexID, StartTurn: 1}, {Agent: protocol.AgentClaude, BackendID: claudeID, Started: true, StartTurn: 2}}}
	if err := agentrouter.StoreThread(thread); err != nil {
		t.Fatal(err)
	}
	rollout := "sessions/2026/11/16/rollout-2026-11-16T10-00-00-" + codexID + ".jsonl"
	writeNative(t, filepath.Join(portable.CodexHome(), rollout), `{"cwd":"`+from+`"}`+"\n")
	writeNative(t, portable.ClaudeSessionPath(from, claudeID), `{"cwd":"`+from+`","x":"`+from+`/a"}`+"\n")

	bundle, err := exportConversation(codexID, from, "laptop")
	if err != nil || len(bundle.Files) != 2 {
		t.Fatalf("export = %d files, %v", len(bundle.Files), err)
	}
	// Import on a machine whose agent homes are empty.
	runnerTestHomes(t)
	native, err := importConversation(bundle, to)
	if err != nil || !native {
		t.Fatalf("import native=%v err=%v", native, err)
	}
	moved := agentrouter.LoadThread(codexID)
	if moved == nil || moved.CWD != to || !moved.Portable || moved.Rebrief || !strings.Contains(moved.Turns[0].User, to+"/main.go") {
		t.Fatalf("imported thread = %+v", moved)
	}
	data, err := os.ReadFile(portable.ClaudeSessionPath(to, claudeID))
	if err != nil || string(data) != `{"cwd":"`+to+`","x":"`+to+`/a"}`+"\n" {
		t.Fatalf("claude session = %q %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(portable.CodexHome(), rollout))
	if err != nil || !strings.Contains(string(data), to) {
		t.Fatalf("codex rollout = %q %v", data, err)
	}
}

func TestConversationBundleFallsBackToBriefing(t *testing.T) {
	runnerTestHomes(t)
	claudeID := "5f0c1a6e-3d2b-4c1a-9e8f-000000000003"
	thread := &agentrouter.Thread{ID: claudeID, CWD: "/a", Turns: []agentrouter.TranscriptTurn{{User: "hi"}},
		Segments: []agentrouter.ThreadSegment{{Agent: protocol.AgentClaude, BackendID: claudeID, Started: true, StartTurn: 1}}}
	if err := agentrouter.StoreThread(thread); err != nil {
		t.Fatal(err)
	}
	bundle, err := exportConversation(claudeID, "/a", "laptop") // no native file exists
	if err != nil || len(bundle.Files) != 0 {
		t.Fatalf("export = %+v %v", bundle.Files, err)
	}
	native, err := importConversation(bundle, "/b")
	if err != nil || native {
		t.Fatalf("import native=%v err=%v", native, err)
	}
	if moved := agentrouter.LoadThread(claudeID); moved == nil || !moved.Rebrief || !moved.RebriefMoved {
		t.Fatalf("missing native session must rebrief: %+v", moved)
	}
}

func makeRunnerWorkspace(t *testing.T, id string) string {
	t.Helper()
	path, err := workspacePath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = fsutil.WriteJSON(path+".json", Workspace{ID: id, Path: path, Ready: true}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunnerLeaseClaimTakeoverAndEpochs(t *testing.T) {
	runnerTestHomes(t)
	makeRunnerWorkspace(t, "ws1")
	paths, err := runnerPathSet("ws1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := claimRunner(Request{ID: "ws1", Runner: "laptop"}, paths)
	if err != nil || first.Epoch != 1 {
		t.Fatalf("first claim = %+v %v", first, err)
	}
	// The laptop is offline (no tunnel socket): a phone needs --take-over.
	if _, err = claimRunner(Request{ID: "ws1", Runner: "desktop"}, paths); err == nil || !strings.Contains(err.Error(), "--take-over") {
		t.Fatalf("claim of an offline device's workspace = %v", err)
	}
	q := Request{ID: "ws1"}
	if _, err = hubRunnerRoute(&q); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("remote session while laptop owns the workspace = %v", err)
	}
	lease, _, _ := loadRunnerLease(paths, "ws1")
	lease.Writer, lease.Conversation = "laptop", "thread-1"
	if err = storeRunnerCheckpoint(paths, &lease, runnerBundle{ThreadID: "thread-1"}, "laptop"); err != nil {
		t.Fatal(err)
	}
	q = Request{ID: "ws1", RunnerTakeover: true}
	if conn, err := hubRunnerRoute(&q); conn != nil || err != nil {
		t.Fatalf("forced takeover = %v %v", conn, err)
	}
	lease, _, _ = loadRunnerLease(paths, "ws1")
	if lease.Owner != "" || lease.Epoch != 2 || lease.LastRunner != runnerHub {
		t.Fatalf("lease after takeover = %+v", lease)
	}
	if lease.holds("laptop", first.Epoch) {
		t.Fatal("the replaced device still holds the lease")
	}
}

// A live device is asked to release; the fake device answers like a local
// runner would after its final checkpoint.
func TestRunnerClaimAsksLiveDeviceToRelease(t *testing.T) {
	runnerTestHomes(t)
	makeRunnerWorkspace(t, "ws2")
	paths, err := runnerPathSet("ws2")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := claimRunner(Request{ID: "ws2", Runner: "laptop"}, paths)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", claim.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = json.NewEncoder(conn).Encode(map[string]string{"method": "jara/ready"})
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- strings.TrimSpace(line)
		unlock, _ := lockRunner(paths)
		lease, _, _ := loadRunnerLease(paths, "ws2")
		lease.Owner = ""
		_ = saveRunnerLease(paths, lease)
		unlock()
	}()
	second, err := claimRunner(Request{ID: "ws2", Runner: "desktop"}, paths)
	if err != nil || second.Epoch != claim.Epoch+1 {
		t.Fatalf("second claim = %+v %v", second, err)
	}
	if message := <-got; message != `{"method":"jara/release"}` {
		t.Fatalf("release request = %s", message)
	}
}
