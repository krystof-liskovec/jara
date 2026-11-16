package jara

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentrouter"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/portable"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
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

func TestRunnerLeaseWaitsForOnlineAndReplacesOfflineDevices(t *testing.T) {
	runnerTestHomes(t)
	makeRunnerWorkspace(t, "ws1")
	paths, err := runnerPathSet("ws1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := claimRunner(Request{ID: "ws1", Runner: "laptop"}, paths)
	if err != nil || first.Epoch != 1 || first.Wait {
		t.Fatalf("first claim = %+v %v", first, err)
	}
	// The laptop renewed its lease just now: others wait, and a window that
	// reconnects by itself never displaces it.
	for _, q := range []Request{{ID: "ws1", Runner: "desktop", Wait: true}, {ID: "ws1", Runner: "desktop", Wait: true, Passive: true}} {
		if claim, err := claimRunner(q, paths); err != nil || !claim.Wait || claim.Runner != "laptop" {
			t.Fatalf("claim of an online device's workspace (%+v) = %+v %v", q, claim, err)
		}
	}
	if _, err = claimRunner(Request{ID: "ws1", Runner: "desktop"}, paths); err == nil {
		t.Fatal("a claim that may not wait took an online device's workspace")
	}
	// An idle device that went offline keeps the workspace until someone
	// connects elsewhere.
	lease, _ := updateRunnerLease(paths, "ws1", func(lease *runnerLease) error {
		lease.LastSeen = time.Now().Add(-time.Hour).Unix()
		return storeRunnerCheckpoint(paths, lease, runnerCheckpoint{Current: "thread-1"}, "laptop")
	})
	if err = continueOnHub("ws1", "went offline", func(runnerLease) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if current, _, _ := loadRunnerLease(paths, "ws1"); current.Owner != "laptop" || current.Epoch != lease.Epoch {
		t.Fatalf("idle offline device was replaced: %+v", current)
	}
	q := Request{ID: "ws1"}
	if conn, err := hubRunnerRoute(&q); conn != nil || err != nil {
		t.Fatalf("connecting on the host = %v %v", conn, err)
	}
	lease, _, _ = loadRunnerLease(paths, "ws1")
	if lease.Owner != "" || lease.Epoch != 2 || lease.LastRunner != runnerHub || lease.HubSeq != lease.Seq {
		t.Fatalf("lease after replacing the offline device = %+v", lease)
	}
	if lease.holds("laptop", first.Epoch) {
		t.Fatal("the replaced device still holds the lease")
	}
}

func TestRunnerCheckpointExportsChangesSince(t *testing.T) {
	runnerTestHomes(t)
	paths, err := runnerPathSet("ws3")
	if err != nil {
		t.Fatal(err)
	}
	bundle := func(id string) runnerBundle { return runnerBundle{ThreadID: id, Thread: json.RawMessage(`{}`)} }
	job := chatSchedule{ID: "sc-1", ThreadID: "a", Prompt: "later", Status: "pending", RunAt: time.Now().Add(time.Hour)}
	lease := runnerLease{Workspace: "ws3"}
	if err = storeRunnerCheckpoint(paths, &lease, runnerCheckpoint{Current: "a", Threads: []runnerBundle{bundle("a"), bundle("b")}}, "laptop"); err != nil {
		t.Fatal(err)
	}
	seen := lease.Seq
	if err = storeRunnerCheckpoint(paths, &lease, runnerCheckpoint{Current: "b", Threads: []runnerBundle{bundle("b")}, Schedules: []chatSchedule{job}}, "laptop"); err != nil {
		t.Fatal(err)
	}
	if err = saveRunnerLease(paths, lease); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := exportRunnerCheckpoint(paths, "ws3", seen)
	if err != nil || checkpoint.Seq != 2 || checkpoint.Current != "b" || len(checkpoint.Threads) != 1 || checkpoint.Threads[0].ThreadID != "b" {
		t.Fatalf("export since %d = %+v %v", seen, checkpoint, err)
	}
	if len(checkpoint.Schedules) != 1 || checkpoint.Schedules[0].ID != "sc-1" {
		t.Fatalf("pending schedules = %+v", checkpoint.Schedules)
	}
	if all, _ := exportRunnerCheckpoint(paths, "ws3", 0); len(all.Threads) != 2 {
		t.Fatalf("full export has %d conversations", len(all.Threads))
	}
	schedules := filepath.Join(t.TempDir(), "schedules.json")
	if err = replaceSchedules(schedules, checkpoint.Schedules); err != nil || len(pendingSchedules(schedules)) != 1 {
		t.Fatalf("schedules round trip: %v %v", pendingSchedules(schedules), err)
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
