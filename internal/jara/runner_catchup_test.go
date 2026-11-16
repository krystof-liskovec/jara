package jara

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/portable"
	"krystof.liskovec/jara/internal/protocol"
)

// A conversation goes laptop → host (the laptop slept) → laptop, and each
// move after the first carries only the lines added since.
func TestHandBackAndTakeBackMoveOnlyNewSessionLines(t *testing.T) {
	base := runnerTestHomes(t)
	on := func(machine string) {
		t.Setenv("JARA_DATA", filepath.Join(base, machine, "d"))
		t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, machine, "claude"))
		t.Setenv("CODEX_HOME", filepath.Join(base, machine, "codex"))
	}
	laptopRoot, hostRoot := "/Users/me/src/project", "/home/agent/workspaces/w-1"
	id := "5f0c1a6e-3d2b-4c1a-9e8f-000000000010"
	line := func(root, text string) string { return `{"cwd":"` + root + `","text":"` + text + `"}` + "\n" }
	session := func(root string) string { return portable.ClaudeSessionPath(root, id) }
	read := func(path string) string { data, _ := os.ReadFile(path); return string(data) }
	appendLine := func(path, text string) {
		writeNative(t, path, read(path)+text)
	}
	checkpoint := func(root, writer string, sent *runnerSentState) runnerCheckpoint {
		checkpoint, _, err := collectCheckpoint(root, writer, id, nil, sent)
		if err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}
	store := func(checkpoint runnerCheckpoint, writer string) runnerLease {
		on("host")
		paths, _ := runnerPathSet("w-1")
		lease, err := updateRunnerLease(paths, "w-1", func(lease *runnerLease) error {
			return storeRunnerCheckpoint(paths, lease, checkpoint, writer)
		})
		if err != nil {
			t.Fatal(err)
		}
		return lease
	}
	sessionBytes := func(checkpoint runnerCheckpoint) (int, int64) {
		file := checkpoint.Threads[0].Files[0]
		return file.size(), file.Base
	}

	// The laptop runs the conversation and checkpoints it.
	on("laptop")
	thread := &agentrouter.Thread{ID: id, CWD: laptopRoot, Segments: []agentrouter.ThreadSegment{{Agent: protocol.AgentClaude, BackendID: id, Started: true, StartTurn: 1}}}
	if err := agentrouter.StoreThread(thread); err != nil {
		t.Fatal(err)
	}
	writeNative(t, session(laptopRoot), strings.Repeat(line(laptopRoot, "laptop work"), 200))
	first := store(checkpoint(laptopRoot, "laptop", nil), "laptop")

	// The laptop sleeps; the host imports, continues and checkpoints.
	paths, _ := runnerPathSet("w-1")
	exported, err := exportRunnerCheckpoint(paths, "w-1", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = importCheckpoint(exported, hostRoot); err != nil {
		t.Fatal(err)
	}
	appendLine(session(hostRoot), line(hostRoot, "host continued")+line(hostRoot, "host again"))
	store(checkpoint(hostRoot, runnerHub, nil), runnerHub)

	// The laptop takes it back: only the host's two lines come.
	on("laptop")
	haves := collectHaves(laptopRoot, hostRoot, []string{id})
	on("host")
	back, err := exportRunnerCheckpoint(paths, "w-1", first.Seq, haves)
	if err != nil {
		t.Fatal(err)
	}
	if size, base := sessionBytes(back); base == 0 || size != len(line(hostRoot, "host continued")+line(hostRoot, "host again")) {
		t.Fatalf("take-back sent %d bytes from offset %d", size, base)
	}
	on("laptop")
	if _, err = importCheckpoint(back, laptopRoot); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat(line(laptopRoot, "laptop work"), 200) + line(laptopRoot, "host continued") + line(laptopRoot, "host again")
	if got := read(session(laptopRoot)); got != want {
		t.Fatalf("laptop session after take-back:\n%q", got[len(got)-200:])
	}

	// The laptop continues: its next checkpoint is a delta again, which the
	// host applies to its copy stored with its own paths.
	sent := &runnerSentState{Prev: sentAfterImport(nil, back), Next: map[string]runnerSentFile{}, Verify: true}
	appendLine(session(laptopRoot), line(laptopRoot, "laptop again"))
	next := checkpoint(laptopRoot, "laptop", sent)
	if size, base := sessionBytes(next); base == 0 || size != len(line(laptopRoot, "laptop again")) {
		t.Fatalf("first checkpoint after take-back sent %d bytes from offset %d", size, base)
	}
	store(next, "laptop")
	on("host")
	stored := read(paths.storedNativePath(id, runnerNativeFile{Agent: protocol.AgentClaude, BackendID: id}))
	on("laptop")
	if stored != read(session(laptopRoot)) {
		t.Fatal("host copy differs from the laptop's after the delta")
	}

	// A laptop file that changed since it was described makes the take-back
	// fall back to the whole file instead of corrupting it.
	on("host")
	appendLine(session(hostRoot), line(hostRoot, "unrelated"))
	on("laptop")
	haves = collectHaves(laptopRoot, hostRoot, []string{id})
	appendLine(session(laptopRoot), line(laptopRoot, "written meanwhile"))
	on("host")
	stale, _ := exportRunnerCheckpoint(paths, "w-1", 0, haves)
	on("laptop")
	if _, err = importCheckpoint(stale, laptopRoot); err != errStaleDelta && !strings.Contains(errString(err), errStaleDelta.Error()) {
		t.Fatalf("stale delta applied: %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
