package jara

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// isolatedGit keeps the user's Git configuration (signing, hooks, templates)
// out of the test repositories.
func isolatedGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(name, "Test")
	}
	for _, name := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(name, "test@example.com")
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeTestFile(t *testing.T, dir, name, text string, modified time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func gitChangeSummary(changes []protocol.FileChange) string {
	var parts []string
	for _, change := range changes {
		parts = append(parts, change.Path+"="+chatFileChangeKind(change.Kind))
	}
	return strings.Join(parts, " ")
}

func TestCollectChatGitDiff(t *testing.T) {
	isolatedGit(t)
	if result, err := collectChatGitDiff(t.TempDir()); err != nil || result.Git {
		t.Fatalf("outside Git = %+v %v", result, err)
	}

	repo := t.TempDir()
	runTestGit(t, repo, "init", "-q")
	// Settings that change diff headers must not confuse the parser.
	runTestGit(t, repo, "config", "diff.mnemonicPrefix", "true")
	runTestGit(t, repo, "config", "color.diff", "always")
	runTestGit(t, repo, "config", "core.quotepath", "true")
	old := time.Now().Add(-time.Hour)
	writeTestFile(t, repo, "keep.go", "package a\n\nfunc A() {}\n", old)
	// Before the first commit, everything is new.
	result, err := collectChatGitDiff(filepath.Join(repo))
	if err != nil || !result.Git || gitChangeSummary(result.Changes) != "keep.go=added" || !strings.Contains(result.Changes[0].Diff, "+func A() {}") {
		t.Fatalf("no commits yet = %+v %v", result, err)
	}

	writeTestFile(t, repo, "gone.go", "package a\n", old)
	writeTestFile(t, repo, "moved.go", "package a\n\nconst Moved = 1\n", old)
	runTestGit(t, repo, "add", "-A")
	runTestGit(t, repo, "commit", "-q", "-m", "base")

	writeTestFile(t, repo, "keep.go", "package a\n\nfunc A() { _ = 1 }\n", old.Add(10*time.Minute))
	if err = os.Remove(filepath.Join(repo, "gone.go")); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "mv", "moved.go", "renamed.go")
	writeTestFile(t, repo, "sub/new.txt", "hello\n", old.Add(20*time.Minute))

	result, err = collectChatGitDiff(filepath.Join(repo, "sub")) // from a subdirectory too
	if err != nil {
		t.Fatal(err)
	}
	got := gitChangeSummary(result.Changes)
	for _, want := range []string{"keep.go=updated", "gone.go=deleted", "renamed.go=rename", "sub/new.txt=added"} {
		if !strings.Contains(got, want) {
			t.Fatalf("changes %q lack %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "sub/new.txt=added keep.go=updated") {
		t.Fatalf("changes are not newest first: %q", got)
	}
	for _, change := range result.Changes {
		if strings.Contains(change.Diff, "diff --git") || strings.Contains(change.Diff, "+++ ") {
			t.Fatalf("file headers leaked into %s: %q", change.Path, change.Diff)
		}
	}

	// After committing everything the pane has nothing to show.
	runTestGit(t, repo, "add", "-A")
	runTestGit(t, repo, "commit", "-q", "-m", "all")
	if result, err = collectChatGitDiff(repo); err != nil || !result.Git || len(result.Changes) != 0 {
		t.Fatalf("after commit = %+v %v", result, err)
	}
}

func TestChangesPaneFollowsGit(t *testing.T) {
	var wire bytes.Buffer
	u := &chatUI{c: &appConnection{encoder: json.NewEncoder(&wire)}, out: &bytes.Buffer{}, pending: map[string]chatPending{}}
	u.diff.record([]protocol.FileChange{{Path: "agent.go", Diff: "+a\n"}}, false)

	u.refreshGitDiff()
	u.refreshGitDiff() // coalesces with the request in flight
	if strings.Count(wire.String(), "jara/git/diff") != 1 || !u.diff.git.again {
		t.Fatalf("requests: %s", wire.String())
	}
	reply := func(result any) {
		data, _ := json.Marshal(result)
		u.acceptGitDiffResponse(appMessage{Result: data})
	}
	reply(chatGitDiffResult{Git: true, Changes: []protocol.FileChange{{Path: "dirty.go", Diff: "+x\n"}}, Omitted: 3})
	if strings.Count(wire.String(), "jara/git/diff") != 2 {
		t.Fatal("the coalesced refresh was not sent")
	}
	lines := chatDiffLines(&u.diff, 60)
	if !strings.HasPrefix(lines[0].text, " Uncommitted · 1 file (+3 not shown)") || !strings.Contains(lines[2].text, "dirty.go") {
		t.Fatalf("git pane: %+v", lines[:3])
	}
	if question := chatDiffQuestion(&u.diff); question != "About the uncommitted changes to dirty.go: " {
		t.Fatalf("question = %q", question)
	}
	reply(chatGitDiffResult{Git: true})
	if lines = chatDiffLines(&u.diff, 60); !strings.Contains(lines[2].text, "Working tree is clean.") {
		t.Fatalf("clean pane: %+v", lines)
	}

	// A session that cannot answer leaves the agent's edits in place.
	other := &chatUI{c: &appConnection{encoder: json.NewEncoder(&bytes.Buffer{})}, out: &bytes.Buffer{}, pending: map[string]chatPending{}}
	other.diff.record([]protocol.FileChange{{Path: "agent.go", Diff: "+a\n"}}, false)
	other.refreshGitDiff()
	var unsupported appMessage
	_ = json.Unmarshal([]byte(`{"id":1,"error":{"message":"unsupported jara/git/diff"}}`), &unsupported)
	other.acceptGitDiffResponse(unsupported)
	if lines = chatDiffLines(&other.diff, 60); !strings.HasPrefix(lines[0].text, " Changes · 1 file") {
		t.Fatalf("fallback pane: %+v", lines[:1])
	}
	other.refreshGitDiff()
	if other.diff.git.requesting {
		t.Fatal("asked again after the session said it cannot answer")
	}
}

func TestHistoryReplayDoesNotRepeatChanges(t *testing.T) {
	u := &chatUI{out: &bytes.Buffer{}}
	turns := []protocol.HistoryTurn{{ID: "t", Items: []protocol.HistoryItem{{ID: "f", Type: "fileChange", Changes: []protocol.FileChange{{Path: "a.go", Diff: "+a\n"}}}}}}
	u.renderChatHistory(turns)
	u.renderChatHistory(turns) // a reconnect replays the same conversation
	if len(u.diff.changes) != 1 {
		t.Fatalf("changes after replaying twice: %d", len(u.diff.changes))
	}
	u.renderChatHistory(nil) // another conversation without edits
	if len(u.diff.changes) != 0 {
		t.Fatalf("another conversation kept %d changes", len(u.diff.changes))
	}
}

func TestDaemonAnswersGitDiff(t *testing.T) {
	isolatedGit(t)
	repo := shortTempDir(t)
	runTestGit(t, repo, "init", "-q")
	writeTestFile(t, repo, "a.txt", "hi\n", time.Now())
	h := startDaemonHarness(t, daemonHarnessOptions{state: chatDaemonState{Conversation: "th-0", CWD: repo}})
	client, _ := h.attach()
	client.send(map[string]any{"id": 9, "method": "jara/git/diff", "params": map[string]any{}})
	for {
		message := client.next()
		if message["id"] != float64(9) {
			continue
		}
		if !strings.Contains(jsonString(t, message["result"]), `"path":"a.txt"`) {
			t.Fatalf("git diff reply = %v", message)
		}
		break
	}
	// The session answers itself; the agent's app-server never sees it.
	for drained := false; !drained; {
		select {
		case message := <-h.server.received:
			if message["method"] == "jara/git/diff" {
				t.Fatal("jara/git/diff reached the app-server")
			}
		default:
			drained = true
		}
	}
	client.send(map[string]string{"method": "jara/close"})
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}
