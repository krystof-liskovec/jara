package jara

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/fsutil"
)

func TestPrepareChatDaemonForCodexWithoutRunningDaemon(t *testing.T) {
	root := useChatDaemonData(t)
	if _, _, err := prepareChatDaemonForCodex("bad/id"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if state, has, err := prepareChatDaemonForCodex("w-1"); err != nil || has || state.WorkspaceID != "" {
		t.Fatalf("no saved session = %+v %v %v", state, has, err)
	}

	// A stopped daemon's saved state restores missing workspace metadata.
	workspace, _ := workspacePath("w-1")
	if err := os.MkdirAll(filepath.Join(workspace, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := chatDaemonTestPaths(t, "w-1")
	saved := chatDaemonState{WorkspaceID: "w-1", Workspace: "demo", CWD: filepath.Join(workspace, "sub"), Conversation: "th-7", CreatedAt: 1_700_000_000_000_000_000}
	if err := writeChatDaemonState(paths, saved); err != nil {
		t.Fatal(err)
	}
	state, has, err := prepareChatDaemonForCodex("w-1")
	if err != nil || !has || state.Conversation != "th-7" || state.CWD != filepath.Join(workspace, "sub") {
		t.Fatalf("stopped daemon = %+v %v %v", state, has, err)
	}
	info, err := workspaceInfo("w-1")
	if err != nil || !info.Ready || info.Name != "demo" || info.Path != workspace || info.Created != "2023-11-14T22:13:20Z" {
		t.Fatalf("recovered workspace = %+v %v", info, err)
	}

	// Saved state that cannot rebuild the metadata is reported.
	if err = os.Remove(workspace + ".json"); err != nil {
		t.Fatal(err)
	}
	saved.CWD = root
	if err = writeChatDaemonState(paths, saved); err != nil {
		t.Fatal(err)
	}
	if _, has, err = prepareChatDaemonForCodex("w-1"); !has || err == nil || !strings.Contains(err.Error(), "outside its workspace") {
		t.Fatalf("unrecoverable workspace = %v %v", has, err)
	}
}

func TestPrepareChatDaemonForCodexRefusesBusyDaemon(t *testing.T) {
	useChatDaemonData(t)
	for _, test := range []struct {
		state chatDaemonState
		want  string
	}{
		{chatDaemonState{ActiveTurn: true}, "turn is still running"},
		{chatDaemonState{Attached: true}, "another client is attached"},
		{chatDaemonState{AttachedClients: 2}, "another client is attached"},
		{chatDaemonState{}, "not accepting a handoff"},
	} {
		registerChatDaemon(t, "w-1", test.state)
		state, has, err := prepareChatDaemonForCodex("w-1")
		if !has || err == nil || !strings.Contains(err.Error(), test.want) || state.WorkspaceID != "w-1" {
			t.Fatalf("state %+v: got %+v %v %v, want %q", test.state, state, has, err, test.want)
		}
	}
	paths := chatDaemonTestPaths(t, "w-1")
	if err := os.WriteFile(paths.state, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, has, err := prepareChatDaemonForCodex("w-1"); has || err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("corrupt state = %v %v", has, err)
	}
	if err := os.Remove(paths.state); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.state, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareChatDaemonForCodex("w-1"); err == nil {
		t.Fatal("unreadable state path accepted")
	}
}

func TestRecoverChatDaemonWorkspaceMetadataValidatesSavedState(t *testing.T) {
	root := useChatDaemonData(t)
	paths := chatDaemonTestPaths(t, "w-1")
	workspace, _ := workspacePath("w-1")
	write := func(state chatDaemonState) {
		t.Helper()
		if err := writeChatDaemonState(paths, state); err != nil {
			t.Fatal(err)
		}
	}
	expectError := func(want string) {
		t.Helper()
		if err := recoverChatDaemonWorkspaceMetadata("w-1", paths); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("got %v, want %q", err, want)
		}
	}
	expectError("state is unavailable")
	write(chatDaemonState{WorkspaceID: "w-other", CWD: workspace})
	expectError("cannot recover")
	write(chatDaemonState{WorkspaceID: "w-1", CWD: workspace})
	expectError("no such file")

	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	write(chatDaemonState{WorkspaceID: "w-1", CWD: filepath.Join(workspace, "gone")})
	expectError("saved session directory is unavailable")
	if err := os.WriteFile(filepath.Join(workspace, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	write(chatDaemonState{WorkspaceID: "w-1", CWD: filepath.Join(workspace, "file")})
	expectError("not a directory")

	// Worktree sessions require the workspace to be its own Git root.
	worktree := filepath.Join(root, "worktrees", "wt")
	if err := os.MkdirAll(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	write(chatDaemonState{WorkspaceID: "w-1", CWD: worktree})
	t.Setenv("PATH", t.TempDir()) // no git: the workspace cannot be verified
	expectError("not a valid Git root")

	write(chatDaemonState{WorkspaceID: "w-1", CWD: workspace})
	if err := recoverChatDaemonWorkspaceMetadata("w-1", paths); err != nil {
		t.Fatal(err)
	}
	info, err := workspaceInfo("w-1")
	if err != nil || info.Name != "w-1" || !info.Ready || info.Created == "" {
		t.Fatalf("metadata = %+v %v", info, err)
	}
	// Existing metadata is never overwritten.
	write(chatDaemonState{WorkspaceID: "w-other"})
	if err = recoverChatDaemonWorkspaceMetadata("w-1", paths); err != nil {
		t.Fatalf("existing metadata = %v", err)
	}
	if err = recoverChatDaemonWorkspaceMetadata("bad/id", paths); err == nil {
		t.Fatal("invalid ID accepted")
	}

	// A symlinked workspace directory is refused.
	if err = os.Remove(workspace + ".json"); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(t.TempDir(), workspace); err != nil {
		t.Fatal(err)
	}
	write(chatDaemonState{WorkspaceID: "w-1", CWD: workspace})
	expectError("not a regular directory")
}

func TestReadSavedChatDaemonState(t *testing.T) {
	useChatDaemonData(t)
	paths := chatDaemonTestPaths(t, "w-1")
	if _, has, err := readSavedChatDaemonState("w-1", paths); has || err != nil {
		t.Fatalf("missing state = %v %v", has, err)
	}
	if err := writeChatDaemonState(paths, chatDaemonState{WorkspaceID: "w-2"}); err != nil {
		t.Fatal(err)
	}
	if _, has, err := readSavedChatDaemonState("w-1", paths); has || err != nil {
		t.Fatalf("foreign state = %v %v", has, err)
	}
	if err := writeChatDaemonState(paths, chatDaemonState{WorkspaceID: "w-1", CWD: "\t/w \n"}); err != nil {
		t.Fatal(err)
	}
	if state, has, err := readSavedChatDaemonState("w-1", paths); !has || err != nil || state.CWD != "/w" {
		t.Fatalf("saved state = %+v %v %v", state, has, err)
	}
}

func TestRunChatDaemonRejectsUnusableWorkspaces(t *testing.T) {
	useChatDaemonData(t)
	useFakeDaemonTools(t, "jara-x|w-managed|0|0|0\n")
	if err := runChatDaemon(Request{UserInterface: "codex", ID: "w-1"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("non-Jara interface = %v", err)
	}
	run := func(id string) error {
		t.Helper()
		registerChatDaemon(t, id, chatDaemonState{})
		err := runChatDaemon(Request{UserInterface: "jara", ID: id})
		if chatDaemonRunning(id) {
			t.Fatalf("%s: failed daemon kept its pid file", id)
		}
		if lock, _ := workspacePath(id); fileExists(lock + ".lock") {
			t.Fatalf("%s: failed daemon kept the workspace lock", id)
		}
		return err
	}
	expect := func(id, want string) {
		t.Helper()
		if err := run(id); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: got %v, want %q", id, err, want)
		}
	}

	createChatDaemonWorkspace(t, "w-unready", false)
	expect("w-unready", "initialization is unfinished")

	path := createChatDaemonWorkspace(t, "w-moved", true)
	if err := fsutil.WriteJSON(path+".json", Workspace{ID: "w-moved", Path: path + "-elsewhere", Ready: true}); err != nil {
		t.Fatal(err)
	}
	expect("w-moved", "does not match")

	path = createChatDaemonWorkspace(t, "w-gone", true)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	expect("w-gone", "workspace is unavailable")

	createChatDaemonWorkspace(t, "w-sched", true)
	if err := os.WriteFile(chatDaemonTestPaths(t, "w-sched").schedules, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	expect("w-sched", "load scheduled prompts")

	createChatDaemonWorkspace(t, "w-crunch", true)
	if err := os.WriteFile(chatDaemonTestPaths(t, "w-crunch").crunch, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	expect("w-crunch", "load crunch task")

	createChatDaemonWorkspace(t, "w-managed", true)
	expect("w-managed", "remote-managed session")

	createChatDaemonWorkspace(t, "w-subdir", true)
	registerChatDaemon(t, "w-subdir", chatDaemonState{})
	if err := runChatDaemon(Request{UserInterface: "jara", ID: "w-subdir", Subdir: "missing"}); err == nil || !strings.Contains(err.Error(), "working directory is unavailable") {
		t.Fatalf("missing subdirectory = %v", err)
	}

	// Without saved state there is nothing to rebuild missing metadata from.
	expect("w-nometa", "recover workspace metadata")

	// A live workspace operation keeps the daemon from starting.
	createChatDaemonWorkspace(t, "w-busy", true)
	unlock, err := lockWorkspace("w-busy")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	registerChatDaemon(t, "w-busy", chatDaemonState{})
	if err = runChatDaemon(Request{UserInterface: "jara", ID: "w-busy"}); err == nil || !strings.Contains(err.Error(), "operation is active") {
		t.Fatalf("locked workspace = %v", err)
	}
}

func TestEnsureChatDaemonReusesListeningDaemon(t *testing.T) {
	useChatDaemonData(t)
	if _, _, err := ensureChatDaemon(Request{ID: "bad/id"}); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if _, err := connectExistingChatDaemon("w-1"); err == nil || !strings.Contains(err.Error(), "no persistent Jara session") {
		t.Fatalf("connect without daemon = %v", err)
	}
	paths := registerChatDaemon(t, "w-1", chatDaemonState{})
	listener, err := net.Listen("unix", paths.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	conn, managed, err := ensureChatDaemon(Request{ID: "w-1"})
	if err != nil || managed || conn == nil {
		t.Fatalf("ensure = %v %v %v", conn, managed, err)
	}
	_ = conn.Close()
	if _, _, err = ensureChatDaemon(Request{ID: "w-1", Worktree: true}); err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("worktree against running daemon = %v", err)
	}
	if conn, err = connectExistingChatDaemon("w-1"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestRunRemoteChatServerReportsTmuxManagedWorkspace(t *testing.T) {
	useChatDaemonData(t)
	useFakeDaemonTools(t, "jara-x|w-managed|1|0|0\n")
	createChatDaemonWorkspace(t, "w-managed", true)
	_, stdout := swapStdio(t)
	if err := runRemoteChatServer(Request{UserInterface: "jara", ID: "w-managed"}); err != nil {
		t.Fatal(err)
	}
	ready := readJSONLine(t, stdout)
	if ready["method"] != "jara/ready" || jsonString(t, ready["params"]) != `{"cwd":"","managed_session":true,"workspace":""}` {
		t.Fatalf("managed ready = %v", ready)
	}
	if paths := chatDaemonTestPaths(t, "w-managed"); fileExists(paths.start) || fileExists(paths.pid) {
		t.Fatal("startup markers were left behind")
	}
	if err := runRemoteChatServer(Request{UserInterface: "codex", ID: "w-managed"}); err == nil {
		t.Fatal("non-Jara interface accepted")
	}
}
