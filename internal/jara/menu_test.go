package jara

import (
	"fmt"
	"github.com/rivo/uniseg"
	"strings"
	"testing"
)

func TestMenuFitsTerminal(t *testing.T) {
	var items []menuItem
	for i := 0; i < 30; i++ {
		items = append(items, menuItem{Title: fmt.Sprintf("長い名前 👩‍💻 café project %d with a very long label", i), Detail: "Running · detached · abc12345 · feature/a-long-branch-name", Workspace: "w-" + strings.Repeat("1", 24)})
	}
	for _, width := range []int{16, 20, 32, 40, 59, 60, 80, 120} {
		for _, height := range []int{7, 8, 12, 24, 40} {
			for selected := range items {
				lines := menuLines("Sessions & workspaces", items, selected, width, height)
				if len(lines) > height {
					t.Fatalf("%dx%d: %d lines", width, height, len(lines))
				}
				found := false
				for _, line := range lines {
					if uniseg.StringWidth(line) > width-1 {
						t.Fatalf("%dx%d overflow: %q", width, height, line)
					}
					if strings.HasPrefix(line, ">") {
						found = true
					}
				}
				if !found {
					t.Fatalf("selection %d missing at %dx%d", selected, width, height)
				}
			}
		}
	}
}
func TestMenuUnicodeAndControlText(t *testing.T) {
	if got := fitMenu("👩‍💻ab", 3); got != "👩‍💻…" {
		t.Fatalf("split grapheme: %q", got)
	}
	if got := fitMenu("éabc", 3); got != "éa…" {
		t.Fatalf("split combining character: %q", got)
	}
	text := menuText("repo\x1b[2J\x1b]0;unsafe\x07\n\u202ename")
	if text != "repo name" {
		t.Fatalf("unsafe terminal text: %q", text)
	}
}
func TestWorkspaceMenuNames(t *testing.T) {
	for _, test := range []struct {
		w    Workspace
		want string
	}{
		{Workspace{Name: "my-copy", URL: "https://git.example/other.git"}, "my-copy"},
		{Workspace{URL: "git@git.example:owner/repository.git"}, "repository"},
		{Workspace{URL: "https://user:secret@git.example/owner/repo.git?token=private"}, "repo"},
		{Workspace{ID: "w-0123456789"}, "Copied workspace"},
	} {
		if got := workspaceTitle(test.w); got != test.want {
			t.Fatalf("got %q; want %q", got, test.want)
		}
	}
}

func TestPhoneMenuKeepsSessionNumberAndHelp(t *testing.T) {
	item := sessionItem(Workspace{ID: "w-012345678901234567890123", Name: strings.Repeat("long-name", 10)}, Session{Attached: 1}, 2)
	lines := menuLines("Sessions", []menuItem{item}, 0, 32, 10)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Running #2", "1-9 open", "Enter open", "q back", "ID: w-012345678901234567890123"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("phone menu hides %q: %s", want, joined)
		}
	}
}
