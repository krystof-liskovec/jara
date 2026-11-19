package jara

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectChatProjectLocation(t *testing.T) {
	isolatedGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	repo := filepath.Join(home, "git-projects", "trace-processor")
	cwd := filepath.Join(repo, "src")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init", "-q", "-b", "fix/customer-section-field-limit")
	path := filepath.Join("~", "git-projects", "trace-processor", "src")
	if got := collectChatProjectLocation(cwd); got != (chatProjectLocation{
		Path: path, Branch: "fix/customer-section-field-limit",
	}) {
		t.Fatalf("project location = %+v", got)
	}
	runTestGit(t, repo, "commit", "--allow-empty", "-qm", "Initial commit")
	runTestGit(t, repo, "checkout", "-q", "--detach", "HEAD")
	if got := collectChatProjectLocation(cwd); got != (chatProjectLocation{Path: path}) {
		t.Fatalf("detached project location = %+v", got)
	}
	if cwd := home + "-elsewhere"; abbreviateChatProjectPath(cwd) != cwd {
		t.Fatalf("abbreviated a sibling of home: %q", cwd)
	}
}

func TestChatProjectLocationUsesPersistentWorkspace(t *testing.T) {
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.cwd, u.persistentSession, u.connected = "/remote/home/git-projects/trace-processor", true, true
	now := time.Now()
	u.refreshChatProjectLocation(now)
	u.refreshChatProjectLocation(now.Add(chatProjectLocationRefresh))
	var request appMessage
	decoder := json.NewDecoder(&wire)
	if err := decoder.Decode(&request); err != nil || request.Method != chatProjectLocationMethod {
		t.Fatalf("project request = %+v, err = %v", request, err)
	}
	if len(u.pending) != 1 {
		t.Fatalf("got %d pending requests while awaiting project location", len(u.pending))
	}
	if err := u.response(appMessage{
		ID: request.ID, Result: json.RawMessage(`{"path":"~/git-projects/trace-processor","branch":"fix/customer-section-field-limit"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if got := u.chatProjectLocation().label(80); got != "~/git-projects/trace-processor on  fix/customer-section-field-limit" {
		t.Fatalf("project label = %q", got)
	}
}

func TestChatProjectLocationAppearsAboveComposer(t *testing.T) {
	project := chatProjectLocation{Path: "~/git-projects/trace-processor", Branch: "fix/customer-section-field-limit"}
	model := chatViewModel{Composer: "draft", CursorColumn: 5, ProjectLocation: project}
	for _, height := range []int{2, 6, 20} {
		frame := renderChatView(model, 80, height, menuPalette{}, false)
		if line := frame.Lines[frame.CursorRow]; !strings.HasPrefix(line, "❯ draft") {
			t.Fatalf("height %d: composer %q", height, line)
		}
		projectRow := frame.CursorRow - 1
		if height >= 9 {
			projectRow-- // top composer border
		}
		if got := strings.TrimSpace(frame.Lines[projectRow]); got != "~/git-projects/trace-processor on  fix/customer-section-field-limit" {
			t.Fatalf("height %d: label = %q", height, got)
		}
	}
	if got := project.label(40); !strings.Contains(got, "trace-processor on  fix/") {
		t.Fatalf("narrow project label = %q", got)
	}
	if got := (chatProjectLocation{Path: "/tmp/project"}).label(40); got != "/tmp/project" {
		t.Fatalf("branchless project label = %q", got)
	}
}
