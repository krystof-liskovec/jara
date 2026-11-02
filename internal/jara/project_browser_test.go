package jara

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConversationProjectsGroupDirectoriesAndSortActivity(t *testing.T) {
	workspaces := []Workspace{{ID: "a", Name: "alpha", Path: "/work/alpha", Ready: true}, {ID: "b", Name: "beta", Path: "/work/beta", Ready: true}}
	threads := []Conversation{{ID: "old", CWD: "/work/alpha", Updated: 1}, {ID: "recent", CWD: "/work/alpha/subdir", Updated: 20}, {ID: "other", CWD: "/outside/project", Updated: 10}}
	groups := conversationProjects(workspaces, threads, []Session{{Name: "terminal", Workspace: "a"}})
	if len(groups) != 3 || projectName(groups[0]) != "alpha" || groups[0].Threads[0].ID != "recent" || len(groups[0].Terminals) != 1 {
		t.Fatalf("incorrect grouping: %+v", groups)
	}
	if projectName(groups[1]) != "project" || projectName(groups[2]) != "beta" {
		t.Fatalf("incorrect recency: %+v", groups)
	}
}

func TestEveryMenuKeepsRowContentStable(t *testing.T) {
	items := []menuItem{{Title: "First prompt", Detail: "Codex · saved"}, {Title: "Second prompt", Detail: "Codex · 1 live"}}
	for _, title := range []string{"Projects", "Credentials", "Deployment"} {
		a := menuLines(title, items, 0, 100, 24)
		b := menuLines(title, items, 1, 100, 24)
		for i := range a {
			if strings.TrimLeft(a[i], "▸ ") != strings.TrimLeft(b[i], "▸ ") {
				t.Fatalf("selection changed content: %q / %q", a[i], b[i])
			}
		}
		if strings.Contains(strings.Join(a, "\n"), "PREVIEW") {
			t.Fatal("preview returned")
		}
	}
}

func TestCodexHistoryHandshakePaginationAndNotifications(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
read -r init
case "$init" in *initialize*) ;; *) exit 2;; esac
printf '%s\n' '{"id":1,"result":{}}'
read -r ready
read -r page
printf '%s\n' '{"method":"notice"}' '{"id":2,"result":{"data":[{"id":"a","cwd":"/project","preview":"First prompt"}],"nextCursor":"next"}}'
read -r page
case "$page" in *next*) ;; *) exit 3;; esac
printf '%s\n' '{"id":3,"result":{"data":[{"id":"a"},{"id":"b","name":"Named conversation"}],"nextCursor":null}}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := (codexConversations{binary: path}).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Preview != "First prompt" || got[1].Name != "Named conversation" {
		t.Fatalf("unexpected conversations: %+v", got)
	}
}

func TestInstalledCodexHistory(t *testing.T) {
	binary := os.Getenv("JARA_CODEX_HISTORY_SMOKE_BINARY")
	if binary == "" {
		t.Skip("set JARA_CODEX_HISTORY_SMOKE_BINARY to check an installed CLI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	threads, err := (codexConversations{binary: binary}).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range threads {
		if thread.ID == "" {
			t.Fatal("missing conversation ID")
		}
	}
	t.Logf("Read %d saved conversations", len(threads))
}
