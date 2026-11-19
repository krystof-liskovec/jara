//go:build linux || darwin

package jara

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rivo/uniseg"
	"krystof.liskovec/jara/internal/protocol"
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
	term.u.diff.screenWidth, term.u.diff.screenHeight = 160, 40
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
	// /diff opens it again, and closes it when shown.
	term.u.runChatDiffCommand()
	if term.u.diff.mode != chatDiffPaneForcedOn {
		t.Fatal("/diff did not reopen the pane")
	}
	term.u.runChatDiffCommand()
	if term.u.diff.mode != chatDiffPaneForcedOff {
		t.Fatal("/diff did not close the pane")
	}
	// Plain output has no pane, so /diff lists the changes instead.
	term.u.diff.screenWidth, term.u.diff.screenHeight = 0, 0
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
	if found != 2 || chatDiffPaneWidth(&term.u.diff, 140, 24) == 0 {
		t.Fatalf("buttons=%d, side pane width %d", found, chatDiffPaneWidth(&term.u.diff, 140, 24))
	}
}

func TestChangesCoverSmallScreensOnlyWhenAsked(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	term, _ := newTestChatTerminal(t)
	files := make([]string, 12)
	for index := range files {
		files[index] = fmt.Sprintf("file%d.go", index)
	}
	term.u.diff = *diffWithFiles(files...)
	for _, size := range [][2]int{{60, 30}, {110, 40}, {160, 15}} {
		frame := term.frame(size[0], size[1], time.Now())
		if strings.Contains(strings.Join(frame.Lines, "\n"), "Changes ·") {
			t.Fatalf("the pane opened by itself at %dx%d", size[0], size[1])
		}
	}

	// On a phone-sized screen, /diff covers the whole screen.
	term.frame(60, 20, time.Now())
	term.u.runChatDiffCommand()
	frame := term.frame(60, 20, time.Now())
	if !strings.HasPrefix(frame.Lines[0], " Changes · 12 files") || frame.CursorRow != -1 || strings.Contains(strings.Join(frame.Lines, "\n"), "❯") {
		t.Fatalf("full-screen changes:\n%s", strings.Join(frame.Lines, "\n"))
	}
	if !strings.Contains(frame.Lines[0], "✕") || frame.target(0, 58) != "diff-close" {
		t.Fatalf("close button %q targets %+v", frame.Lines[0], frame.Targets)
	}
	typeChatKeys(t, term, "\x1b[5~") // PageUp scrolls back from the newest changes
	if term.u.diff.scroll == 0 || !term.u.diff.fullScreen() {
		t.Fatalf("PageUp: scroll %d, full screen %v", term.u.diff.scroll, term.u.diff.fullScreen())
	}
	typeChatKeys(t, term, "\x1b[6~")
	if term.u.diff.scroll != 0 {
		t.Fatalf("PageDown returns to the newest changes: scroll %d", term.u.diff.scroll)
	}
	typeChatKeys(t, term, "x") // typing returns to the conversation
	if term.u.diff.fullScreen() || term.editor.Text() != "x" || term.u.diff.mode != chatDiffPaneAuto {
		t.Fatalf("typing: full screen %v, input %q, mode %d", term.u.diff.fullScreen(), term.editor.Text(), term.u.diff.mode)
	}

	// The close button, F4 and /diff all leave the full-screen view.
	term.u.runChatDiffCommand()
	term.frame(60, 20, time.Now())
	term.targets = term.frame(60, 20, time.Now())
	typeChatKeys(t, term, "\x1b[<0;59;1M")
	if term.u.diff.fullScreen() {
		t.Fatal("✕ did not close the full-screen changes")
	}
	term.u.runChatDiffCommand()
	term.u.runChatDiffCommand()
	if term.u.diff.fullScreen() {
		t.Fatal("/diff did not close the full-screen changes")
	}
	term.u.runChatDiffCommand()
	term.editor.SetText("")
	term.parser.feed([]byte("\x1b")) // a lone Escape resolves on the next tick
	if err := term.tick(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if term.u.diff.mode != chatDiffPaneAuto || term.editor.Text() != "" {
		t.Fatalf("Escape: mode %d, input %q", term.u.diff.mode, term.editor.Text())
	}
}
