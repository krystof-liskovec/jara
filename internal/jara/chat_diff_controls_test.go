//go:build linux || darwin

package jara

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
	"github.com/rivo/uniseg"
)

func diffWithFiles(names ...string) *chatDiffState {
	d := &chatDiffState{}
	for _, name := range names {
		d.record([]protocol.FileChange{{Path: name, Diff: "@@ -1 +1 @@\n-a\n+b\n"}}, false)
	}
	return d
}

// cellText returns the plain characters a target covers on a painted row.
func cellText(row string, from, to int) string {
	var text strings.Builder
	column := 0
	graphemes := uniseg.NewGraphemes(row)
	for graphemes.Next() {
		if column >= from && column < to {
			text.WriteString(graphemes.Str())
		}
		column += graphemes.Width()
	}
	return text.String()
}

func TestDiffPaneTitleButtonsLineUpWithTargets(t *testing.T) {
	d := diffWithFiles("a.go")
	rows := renderChatDiffPane(d, 50, 20, time.Now(), false, menuPalette{}, false)
	if !strings.Contains(rows[0], "[ ask ]") || !strings.HasSuffix(strings.TrimRight(rows[0], " "), "✕") {
		t.Fatalf("title %q", rows[0])
	}
	screen := strings.Repeat(".", 30) + "│" + rows[0]
	targets := chatDiffPaneTargets(d, 50, 20, 31)
	if len(targets) != 2 {
		t.Fatalf("targets = %v", targets)
	}
	for _, target := range targets {
		want := map[string]string{"diff-ask": "[ ask ]", "diff-close": "✕"}[target.action]
		if got := cellText(screen, target.from, target.to); got != want {
			t.Fatalf("%s covers %q, want %q", target.action, got, want)
		}
	}
	empty := renderChatDiffPane(&chatDiffState{}, 50, 20, time.Now(), false, menuPalette{}, false)
	if strings.Contains(empty[0], "ask") || !strings.Contains(empty[0], "✕") {
		t.Fatalf("an empty pane offers only the close button: %q", empty[0])
	}
}

func TestDiffQuestionNamesNewestFiles(t *testing.T) {
	if got := chatDiffQuestion(diffWithFiles("a.go", "b.go")); got != "About your changes to b.go, a.go: " {
		t.Fatalf("question = %q", got)
	}
	if got := chatDiffQuestion(diffWithFiles("1", "2", "3", "4", "5", "2")); got != "About your changes to 2, 5, 4 and 2 more files: " {
		t.Fatalf("question = %q", got)
	}
	if got := chatDiffQuestion(&chatDiffState{}); got != "" {
		t.Fatalf("question without changes = %q", got)
	}
}

func TestDiffPaneButtonsAndCommand(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	term.u.diff = *diffWithFiles("main.go")
	term.u.diff.fits, term.u.diff.shown = true, true
	term.editor.SetText("why")
	term.targets = chatFrame{Targets: []chatTapTarget{{row: 0, from: 80, to: 87, action: "diff-ask"}, {row: 0, from: 90, to: 91, action: "diff-close"}}}
	typeChatKeys(t, term, fmt.Sprintf("\x1b[<0;%d;1M", 82))
	if got := term.editor.Text(); got != "About your changes to main.go: why" {
		t.Fatalf("ask filled %q", got)
	}
	typeChatKeys(t, term, fmt.Sprintf("\x1b[<0;%d;1M", 82))
	if got := term.editor.Text(); strings.Count(got, "About your changes") != 1 {
		t.Fatalf("asking twice repeated the question: %q", got)
	}
	typeChatKeys(t, term, "\x1b[<0;91;1M")
	if term.u.diff.mode != chatDiffPaneForcedOff {
		t.Fatal("✕ did not close the pane")
	}
	// /diff opens it again where it fits, and closes it when shown.
	term.u.diff.shown = false
	term.u.runChatDiffCommand()
	if term.u.diff.mode != chatDiffPaneForcedOn {
		t.Fatal("/diff did not reopen the pane")
	}
	term.u.diff.shown = true
	term.u.runChatDiffCommand()
	if term.u.diff.mode != chatDiffPaneForcedOff {
		t.Fatal("/diff did not close the pane")
	}
	// Without room for the pane, /diff lists the changes instead.
	term.u.diff.fits = false
	before := len(term.transcript.entries)
	term.u.runChatDiffCommand()
	if len(term.transcript.entries) == before {
		t.Fatal("/diff on a narrow terminal listed nothing")
	}
}

func TestComposedScreenTargetsPaneButtons(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	term, _ := newTestChatTerminal(t)
	term.u.diff = *diffWithFiles("main.go")
	frame := term.frame(140, 24, time.Now())
	found := 0
	for _, target := range frame.Targets {
		want := map[string]string{"diff-ask": "[ ask ]", "diff-close": "✕"}[target.action]
		if want == "" {
			continue
		}
		found++
		if got := cellText(frame.Lines[target.row], target.from, target.to); got != want {
			t.Fatalf("%s covers %q on %q", target.action, got, frame.Lines[target.row])
		}
	}
	if found != 2 || !term.u.diff.fits || !term.u.diff.shown {
		t.Fatalf("buttons=%d fits=%v shown=%v", found, term.u.diff.fits, term.u.diff.shown)
	}
}
