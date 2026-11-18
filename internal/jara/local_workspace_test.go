package jara

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

func TestWorkspaceUsesRemoteOnlyWhenNamedOrAttached(t *testing.T) {
	c := Config{Default: "work", Remotes: map[string]Remote{"work": {Host: "h"}},
		Bindings: map[string]Binding{"/src/attached": {Remote: "work", Workspace: "w-1"}}}
	cases := []struct {
		root, flag string
		runRemote  bool
		want       bool
	}{
		{"/src/new", "", false, false}, // a default remote alone does not attach a folder
		{"/src/new", "work", false, true},
		{"/src/new", "", true, true},
		{"/src/attached", "", false, true},
	}
	for _, tc := range cases {
		if got := workspaceUsesRemote(c, tc.root, tc.flag, tc.runRemote); got != tc.want {
			t.Errorf("workspaceUsesRemote(%q, %q, %v) = %v, want %v", tc.root, tc.flag, tc.runRemote, got, tc.want)
		}
	}
}

// Without any remote, the native interface runs the agent CLI from PATH in
// the current folder.
func TestRunWithoutRemoteStartsLocalNativeAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JARA_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("JARA_DATA", filepath.Join(dir, "data"))
	t.Setenv("JARA_CODEX_ARGS", "")
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "record")
	script := "#!/bin/sh\npwd > " + quote(record) + "\necho \"$@\" >> " + quote(record) + "\n"
	for _, name := range []string{"codex", "claude", "ssh"} {
		body := script
		if name == "ssh" {
			body = "#!/bin/sh\necho 'ssh must not run' >&2\nexit 1\n"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(project, "sub")
	if err := Run([]string{"resume", "--interface", "codex", "--agent", "claude", "--source", source}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	resolved, _ := filepath.EvalSymlinks(source)
	if len(lines) != 2 || lines[0] != resolved || lines[1] != "--resume" {
		t.Fatalf("agent ran as %q", lines)
	}
	if _, err = os.Stat(os.Getenv("JARA_CONFIG")); err == nil {
		c, _ := readConfig()
		if len(c.Bindings) != 0 {
			t.Fatalf("a local session attached the folder: %+v", c.Bindings)
		}
	}
}

// A session named after its remote workspace moves to the folder's ID, so
// local-only and attached use of a folder share one session.
func TestLocalSessionIDAdoptsLegacyRunnerSession(t *testing.T) {
	runnerTestHomes(t)
	root := "/src/project"
	legacy := localRunnerID("work", "w-1")
	from, err := chatDaemonPathSet(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeChatDaemonState(from, chatDaemonState{WorkspaceID: legacy, Conversation: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(from.schedules, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = fsutil.WriteJSON(localRunnerMarkerPath(legacy), localRunnerMarker{Remote: "work", Workspace: "w-1", Seq: 7}); err != nil {
		t.Fatal(err)
	}
	id, err := localSessionID(root, "work", "w-1")
	if err != nil {
		t.Fatal(err)
	}
	if id != localWorkspaceID(root) {
		t.Fatalf("session ID %q, want the folder's", id)
	}
	to, _ := chatDaemonPathSet(id)
	state, ok, _ := readSavedChatDaemonState(id, to)
	if !ok || state.Conversation != "thread-1" {
		t.Fatalf("state not adopted: %+v", state)
	}
	if loadLocalRunnerMarker(id).Seq != 7 {
		t.Fatal("marker not adopted")
	}
	if _, err = os.Stat(to.schedules); err != nil {
		t.Fatal("schedules not adopted")
	}
	if _, err = os.Stat(localRunnerMarkerPath(legacy)); !os.IsNotExist(err) {
		t.Fatal("legacy marker left behind")
	}
}

// After catching up, conversations only this device has stay unrecorded so
// the next checkpoint sends them to the host.
func TestVersionsAfterCatchUpKeepLocalOnlyConversationsUnsent(t *testing.T) {
	runnerTestHomes(t)
	root := "/src/project"
	for _, id := range []string{"5f0c1a6e-3d2b-4c1a-9e8f-000000000001", "5f0c1a6e-3d2b-4c1a-9e8f-000000000002", "5f0c1a6e-3d2b-4c1a-9e8f-000000000003"} {
		thread := &agentrouter.Thread{ID: id, CWD: root, Segments: []agentrouter.ThreadSegment{{Agent: protocol.AgentClaude, BackendID: id, Started: true, StartTurn: 1}}}
		if err := agentrouter.StoreThread(thread); err != nil {
			t.Fatal(err)
		}
	}
	known := map[string]string{"5f0c1a6e-3d2b-4c1a-9e8f-000000000001": "old"}
	checkpoint := runnerCheckpoint{Threads: []runnerBundle{{ThreadID: "5f0c1a6e-3d2b-4c1a-9e8f-000000000002"}}}
	versions := versionsAfterCatchUp(root, known, checkpoint)
	if _, ok := versions["5f0c1a6e-3d2b-4c1a-9e8f-000000000003"]; ok || len(versions) != 2 {
		t.Fatalf("versions %v", versions)
	}
}
