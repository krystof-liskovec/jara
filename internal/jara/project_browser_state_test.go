package jara

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFailedHistoryIsNotDisplayedAsZeroChats(t *testing.T) {
	failure := errors.New("history request timed out")
	workspaces := []Workspace{{ID: "a", Path: "/work/a", Ready: true}}
	data := projectBrowserState(workspaces, nil, []Session{{Name: "live", Workspace: "a"}}, failure)
	detail := projectDetail(data.Projects[0])
	if !strings.Contains(detail, "History unavailable") || strings.Contains(detail, "0 chats") || !strings.Contains(detail, "1 terminals") {
		t.Fatalf("misleading history status: %q", detail)
	}
	data = projectBrowserState(workspaces, []Conversation{{ID: "c", CWD: "/work/a"}}, nil, failure)
	detail = projectDetail(data.Projects[0])
	if !strings.Contains(detail, "1+ chats") || !strings.Contains(detail, "incomplete") {
		t.Fatalf("partial history presented as a complete count: %q", detail)
	}
	data = projectBrowserState(workspaces, nil, nil, nil)
	if detail = projectDetail(data.Projects[0]); !strings.Contains(detail, "0 chats") {
		t.Fatalf("successful empty history should show zero: %q", detail)
	}
}

func TestRefreshedProjectCountsAndSelection(t *testing.T) {
	workspaces := []Workspace{{ID: "a", Path: "/work/a"}, {ID: "b", Path: "/work/b"}, {ID: "c", Path: "/work/c"}}
	before := projectBrowserState(workspaces, []Conversation{{ID: "first", CWD: "/work/a", Updated: 1}}, nil, nil)
	after := projectBrowserState(workspaces, []Conversation{
		{ID: "first", CWD: "/work/a", Updated: 1},
		{ID: "second", CWD: "/work/a/subdir", Updated: 2},
		{ID: "third", CWD: "/work/b", Updated: 3},
		{ID: "fourth", CWD: "/work/b/subdir", Updated: 4},
		{ID: "fifth", CWD: "/work/b", Updated: 5},
	}, nil, nil)
	counts := map[string]int{}
	for _, p := range after.Projects {
		counts[p.Workspace.ID] = len(p.Threads)
	}
	if counts["a"] != 2 || counts["b"] != 3 || counts["c"] != 0 {
		t.Fatalf("incorrect per-checkout counts: %v", counts)
	}
	if len(before.Projects[0].Threads) != 1 || after.Projects[0].Workspace.ID != "b" {
		t.Fatal("new activity did not refresh and reorder project data")
	}
	selected := projectBrowserSelection(after.Projects, "a", "/work/a")
	if after.Projects[selected].Workspace.ID != "a" {
		t.Fatal("refresh selected another checkout after activity ordering changed")
	}
}

type partialHistoryProvider struct{}

func (partialHistoryProvider) Name() string                                    { return "Test" }
func (partialHistoryProvider) ResumeCommand(string, string, []string) []string { return nil }
func (partialHistoryProvider) List(context.Context) ([]Conversation, error) {
	return []Conversation{{ID: "saved", CWD: "/work/a"}}, errors.New("later page failed")
}

func TestProviderFailureRetainsPartialConversations(t *testing.T) {
	threads, err := listConversations(context.Background(), []conversationProvider{partialHistoryProvider{}})
	if err == nil || len(threads) != 1 || threads[0].ID != "saved" || threads[0].Agent != "Test" {
		t.Fatalf("partial results lost: %+v, %v", threads, err)
	}
}

func TestCodexHistoryRetainsPagesBeforeAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell app-server fixture")
	}
	binary := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
read -r init
printf '%s\n' '{"id":1,"result":{}}'
read -r ready
read -r page
printf '%s\n' '{"id":2,"result":{"data":[{"id":"saved","cwd":"/work/a"}],"nextCursor":"next"}}'
read -r page
printf '%s\n' '{"id":3,"error":{"code":-32000,"message":"history unavailable"}}'
`
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	threads, err := (codexConversations{binary: binary}).List(ctx)
	if err == nil || len(threads) != 1 || threads[0].ID != "saved" {
		t.Fatalf("lost previously fetched history: %+v, %v", threads, err)
	}
}
