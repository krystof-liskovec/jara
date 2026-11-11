package jara

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// useChatDaemonData points Jara's data root at a short temporary directory.
func useChatDaemonData(t *testing.T) string {
	t.Helper()
	root := shortTempDir(t)
	t.Setenv("JARA_DATA", root)
	return root
}

func chatDaemonTestPaths(t *testing.T, id string) chatDaemonPaths {
	t.Helper()
	paths, err := chatDaemonPathSet(id)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// registerChatDaemon records the current test process as a live daemon.
func registerChatDaemon(t *testing.T, id string, state chatDaemonState) chatDaemonPaths {
	t.Helper()
	paths := chatDaemonTestPaths(t, id)
	if err := os.WriteFile(paths.pid, []byte(strconv.Itoa(os.Getpid())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state.WorkspaceID, state.PID = id, os.Getpid()
	if err := writeChatDaemonState(paths, state); err != nil {
		t.Fatal(err)
	}
	return paths
}

func exitedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestChatDaemonPathSetValidatesLocation(t *testing.T) {
	root := useChatDaemonData(t)
	if _, err := chatDaemonPathSet("../escape"); err == nil {
		t.Fatal("invalid workspace ID accepted")
	}
	paths := chatDaemonTestPaths(t, "w-1")
	base := filepath.Join(root, "chat", "w-1")
	if paths.socket != base+".sock" || paths.state != base+".json" || paths.schedules != base+".schedules.json" || paths.crunch != base+".crunch.json" || paths.pid != base+".pid" || paths.start != base+".start" || paths.log != base+".log" {
		t.Fatalf("paths = %+v", paths)
	}
	if info, err := os.Stat(filepath.Join(root, "chat")); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("chat directory = %v %v", info, err)
	}
	t.Setenv("JARA_DATA", filepath.Join(root, strings.Repeat("d", 40)))
	if _, err := chatDaemonPathSet(strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("overlong socket path = %v", err)
	}

	unsafe := shortTempDir(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(unsafe, "chat")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JARA_DATA", unsafe)
	if _, err := chatDaemonPathSet("w-1"); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("symlinked chat directory = %v", err)
	}
}

func TestChatDaemonPIDIgnoresStaleRecords(t *testing.T) {
	useChatDaemonData(t)
	paths := chatDaemonTestPaths(t, "w-1")
	if chatDaemonPID(paths) != 0 {
		t.Fatal("missing pid file reported a daemon")
	}
	for _, content := range []string{"garbage", "1", strconv.Itoa(exitedPID(t))} {
		if err := os.WriteFile(paths.pid, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if chatDaemonPID(paths) != 0 {
			t.Fatalf("pid %q reported a daemon", content)
		}
		if _, err := os.Stat(paths.pid); !os.IsNotExist(err) {
			t.Fatalf("stale pid %q was kept", content)
		}
	}
	registerChatDaemon(t, "w-1", chatDaemonState{})
	if chatDaemonPID(paths) != os.Getpid() || !chatDaemonRunning("w-1") {
		t.Fatal("live daemon not detected")
	}
	if chatDaemonRunning("w-2") || chatDaemonRunning("bad/id") {
		t.Fatal("unregistered workspace reported running")
	}

	removeChatDaemonPID(paths, os.Getpid()+1)
	if _, err := os.Stat(paths.pid); err != nil {
		t.Fatal("another process's pid file was removed")
	}
	removeChatDaemonPID(paths, os.Getpid())
	if _, err := os.Stat(paths.pid); !os.IsNotExist(err) {
		t.Fatal("own pid file was kept")
	}
}

func TestReadChatDaemonStateRequiresMatchingProcess(t *testing.T) {
	useChatDaemonData(t)
	if _, ok := readChatDaemonState("bad/id"); ok {
		t.Fatal("invalid ID returned state")
	}
	paths := registerChatDaemon(t, "w-1", chatDaemonState{Conversation: "th", ActiveTurn: true})
	state, ok := readChatDaemonState("w-1")
	if !ok || state.Conversation != "th" || !state.ActiveTurn {
		t.Fatalf("state = %+v %v", state, ok)
	}
	for _, mutate := range []func(*chatDaemonState){
		func(s *chatDaemonState) { s.WorkspaceID = "w-other" },
		func(s *chatDaemonState) { s.PID++ },
	} {
		changed := state
		mutate(&changed)
		if err := writeChatDaemonState(paths, changed); err != nil {
			t.Fatal(err)
		}
		if _, ok := readChatDaemonState("w-1"); ok {
			t.Fatalf("mismatched state accepted: %+v", changed)
		}
	}
	if err := os.WriteFile(paths.state, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readChatDaemonState("w-1"); ok {
		t.Fatal("corrupt state accepted")
	}
}

func TestChatDaemonSessionsListsLiveDaemons(t *testing.T) {
	root := useChatDaemonData(t)
	if sessions := chatDaemonSessions(); sessions != nil {
		t.Fatalf("sessions without data = %+v", sessions)
	}
	registerChatDaemon(t, "w-idle", chatDaemonState{Conversation: "th-1", CreatedAt: 42, Attached: true})
	registerChatDaemon(t, "w-0123456789abcdefghij", chatDaemonState{ActiveTurn: true, AttachedClients: 3})
	stale := chatDaemonTestPaths(t, "w-stale")
	if err := writeChatDaemonState(stale, chatDaemonState{WorkspaceID: "w-stale", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "chat", "notes.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	sessions := chatDaemonSessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v", sessions)
	}
	byWorkspace := map[string]Session{}
	for _, session := range sessions {
		byWorkspace[session.Workspace] = session
	}
	idle := byWorkspace["w-idle"]
	if idle.Name != "jara-chat-idle" || !idle.ChatDaemon || idle.Attached != 1 || idle.AgentState != "idle" || idle.Conversation != "th-1" || idle.CreatedAt != 42 {
		t.Fatalf("idle session = %+v", idle)
	}
	busy := byWorkspace["w-0123456789abcdefghij"]
	if busy.Name != "jara-chat-0123456789abcd" || len(busy.Name) != 24 || busy.Attached != 3 || busy.AgentState != "running" {
		t.Fatalf("busy session = %+v", busy)
	}
}

func TestValidateExistingChatDaemonRequest(t *testing.T) {
	useChatDaemonData(t)
	if err := validateExistingChatDaemonRequest(Request{ID: "w-1", Worktree: true}); err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("worktree request = %v", err)
	}
	if err := validateExistingChatDaemonRequest(Request{ID: "w-1", New: true}); err != nil {
		t.Fatalf("new conversation without daemon = %v", err)
	}
	registerChatDaemon(t, "w-1", chatDaemonState{ActiveTurn: true})
	if err := validateExistingChatDaemonRequest(Request{ID: "w-1", New: true}); err == nil || !strings.Contains(err.Error(), "turn is already running") {
		t.Fatalf("new conversation during turn = %v", err)
	}
	if err := validateExistingChatDaemonRequest(Request{ID: "w-1"}); err != nil {
		t.Fatalf("reattach during turn = %v", err)
	}
}

func TestChatDaemonWorkspaceLockMarkers(t *testing.T) {
	useChatDaemonData(t)
	if _, err := markChatDaemonWorkspaceLock("bad/id"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if err := recoverChatDaemonWorkspaceLock("w-1"); err != nil {
		t.Fatalf("missing lock was not recoverable: %v", err)
	}
	unlock, err := lockWorkspace("w-1")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := markChatDaemonWorkspaceLock("w-1")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(marker); strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("marker content %q", data)
	}
	if _, err = markChatDaemonWorkspaceLock("w-1"); err == nil {
		t.Fatal("second daemon marker was created")
	}
	if err = recoverChatDaemonWorkspaceLock("w-1"); err == nil || !strings.Contains(err.Error(), "operation is active") {
		t.Fatalf("live lock recovered: %v", err)
	}
	if err = os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	unlock()

	// A crashed daemon leaves its marker behind; recovery clears the lock.
	workspace, _ := workspacePath("w-1")
	if err = os.Mkdir(workspace+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace+".lock", "chat-daemon.pid"), []byte(strconv.Itoa(exitedPID(t))), 0600); err != nil {
		t.Fatal(err)
	}
	if err = recoverChatDaemonWorkspaceLock("w-1"); err != nil {
		t.Fatalf("stale daemon lock = %v", err)
	}
	if _, err = os.Stat(workspace + ".lock"); !os.IsNotExist(err) {
		t.Fatal("stale lock directory was kept")
	}
	if err = recoverChatDaemonWorkspaceLock("bad/id"); err == nil {
		t.Fatal("invalid ID accepted")
	}
}

func TestChatDaemonWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	workspace := Workspace{ID: "w-1", Path: root}
	for _, test := range []struct {
		subdir, want, err string
	}{
		{"", root, ""},
		{".", root, ""},
		{"sub/dir", filepath.Join(root, "sub", "dir"), ""},
		{"missing", "", "unavailable"},
		{"file", "", "unavailable"},
		{"../outside", "", ""},
	} {
		cwd, err := chatDaemonWorkingDirectory(Request{Subdir: test.subdir}, workspace)
		if test.want != "" {
			if err != nil || cwd != test.want {
				t.Fatalf("subdir %q = %q, %v", test.subdir, cwd, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), test.err) {
			t.Fatalf("subdir %q = %q, %v; want error %q", test.subdir, cwd, err, test.err)
		}
	}
}

func TestChatDaemonPathWithin(t *testing.T) {
	for _, test := range []struct {
		root, path string
		want       bool
	}{
		{"/w", "/w", true},
		{"/w", "/w/a/b", true},
		{"/w", "/w/..a", true},
		{"/w", "/", false},
		{"/w", "/wx", false},
		{"/w", "/w/../x", false},
		{"/w/a", "/w", false},
	} {
		if got := chatDaemonPathWithin(test.root, filepath.Clean(test.path)); got != test.want {
			t.Errorf("chatDaemonPathWithin(%q, %q) = %v", test.root, test.path, got)
		}
	}
}
