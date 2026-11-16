package portable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRewritePathKeepsSiblingDirectories(t *testing.T) {
	input := `{"cwd":"/home/agent/ws/abc","file":"/home/agent/ws/abc/main.go","other":"/home/agent/ws/abcd","cmd":"cd /home/agent/ws/abc && ls"}`
	got := string(RewritePath([]byte(input), "/home/agent/ws/abc", "/Users/me/src/abc"))
	want := `{"cwd":"/Users/me/src/abc","file":"/Users/me/src/abc/main.go","other":"/home/agent/ws/abcd","cmd":"cd /Users/me/src/abc && ls"}`
	if got != want {
		t.Fatalf("rewrite:\n got %s\nwant %s", got, want)
	}
}

func TestRewritePathHandlesEscapedSlashes(t *testing.T) {
	got := string(RewritePath([]byte(`"\/srv\/w\/x"`), "/srv/w", "/l"))
	if got != `"\/l\/x"` {
		t.Fatalf("escaped rewrite = %s", got)
	}
	if string(RewritePath([]byte("same"), "/a", "/a")) != "same" {
		t.Fatal("identical paths must not change data")
	}
}

func TestClaudeProjectName(t *testing.T) {
	if got := ClaudeProjectName("/home/a.b/my_proj"); got != "-home-a-b-my-proj" {
		t.Fatalf("project name = %s", got)
	}
}

func TestFindCodexRolloutAndDestination(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "0199aaaa-bbbb-7ccc-8ddd-eeeeffff0000"
	rel := filepath.Join("archived_sessions", "2026", "11", "16", "rollout-2026-11-16T10-00-00-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Join(home, filepath.Dir(rel)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, rel), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path, found, err := FindCodexRollout(id)
	if err != nil || path != filepath.Join(home, rel) || found != filepath.ToSlash(rel) {
		t.Fatalf("find = %q %q %v", path, found, err)
	}
	dest, err := CodexRolloutDestination(found, id)
	if err != nil || dest != filepath.Join(home, "sessions", "2026", "11", "16", filepath.Base(rel)) {
		t.Fatalf("destination = %q %v", dest, err)
	}
	if _, err = CodexRolloutDestination("../escape-"+id+".jsonl", id); err == nil {
		t.Fatal("escaping destination accepted")
	}
}
