package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

func TestChatDiffPaneLayoutAndOrder(t *testing.T) {
	var d chatDiffState
	if chatDiffPaneWidth(&d, 160, 40) != 0 {
		t.Fatal("auto pane stays hidden without changes")
	}
	raw, _ := json.Marshal(map[string]any{"changes": []protocol.FileChange{{Path: "a.go", Diff: "@@ -1 +1 @@\n-old\n+new\n"}, {Path: "b.go", Diff: "+x\n"}}})
	d.recordFileChanges(raw, true)
	d.record([]protocol.FileChange{{Path: "c.go", Diff: "+later\n"}}, false)
	if got := []string{d.changes[0].Path, d.changes[1].Path, d.changes[2].Path}; strings.Join(got, ",") != "a.go,b.go,c.go" {
		t.Fatalf("order %v", got)
	}
	if chatDiffPaneWidth(&d, 119, 40) != 0 || chatDiffPaneWidth(&d, 160, 17) != 0 {
		t.Fatal("the pane must not open by itself on small terminals")
	}
	width := chatDiffPaneWidth(&d, 160, 40)
	if width < 44 || width > 100 {
		t.Fatalf("pane width %d", width)
	}
	d.toggle(160, 40)
	if chatDiffPaneWidth(&d, 160, 40) != 0 {
		t.Fatal("F4 should hide a visible pane")
	}
	d.toggle(160, 40)
	if chatDiffPaneWidth(&d, 160, 40) == 0 {
		t.Fatal("F4 should show a hidden pane")
	}
	lines := chatDiffLines(&d, 60)
	kinds := map[string]bool{}
	for _, line := range lines {
		kinds[line.kind] = true
	}
	for _, kind := range []string{"pane-title", "pane-file", "pane-hunk", "pane-add", "pane-delete"} {
		if !kinds[kind] {
			t.Fatalf("missing %s in %+v", kind, lines)
		}
	}
	if !strings.Contains(lines[0].text, "3 files · +3 −1") {
		t.Fatalf("title %q", lines[0].text)
	}
}

func TestChatDiffPaneRendersFixedCellsAndReveals(t *testing.T) {
	palette := menuColorsForBackground(terminalRGB{r: 30, g: 30, b: 30})
	var d chatDiffState
	d.record([]protocol.FileChange{{Path: "wide_界.go", Diff: strings.Repeat("+line with\ttab and a long tail that must be clipped\n", 50)}}, true)
	for _, color := range []bool{false, true} {
		rows := renderChatDiffPane(&d, 50, 20, d.arrived.Add(time.Hour), true, palette, color)
		for _, row := range rows {
			if got := uniseg.StringWidth(textutil.TerminalEscapes.ReplaceAllString(row, "")); got != 50 {
				t.Fatalf("row width %d: %q", got, row)
			}
		}
	}
	early := renderChatDiffPane(&d, 50, 20, d.arrived.Add(30*time.Millisecond), true, palette, false)
	late := renderChatDiffPane(&d, 50, 20, d.arrived.Add(time.Hour), true, palette, false)
	if strings.Count(strings.Join(early, "\n"), "+line") >= strings.Count(strings.Join(late, "\n"), "+line") {
		t.Fatal("reveal animation should show fewer rows at first")
	}
	still := renderChatDiffPane(&d, 50, 20, d.arrived.Add(30*time.Millisecond), false, palette, false)
	if strings.Join(still, "\n") != strings.Join(late, "\n") {
		t.Fatal("reduced motion must render the final state immediately")
	}
	d.scroll = 1000
	scrolled := renderChatDiffPane(&d, 50, 20, d.arrived.Add(time.Hour), false, palette, false)
	if !strings.Contains(scrolled[0], "/") {
		t.Fatalf("scroll position marker missing: %q", scrolled[0])
	}
	frame := composeChatScreen(chatFrame{Lines: []string{"left"}}, []string{"right"}, palette, false)
	if frame.Lines[0] != "left│right" {
		t.Fatalf("compose %q", frame.Lines[0])
	}
}

func TestChatAnimations(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("JARA_REDUCED_MOTION", "")
	t.Setenv("TERM", "xterm-256color")
	if !chatMotionEnabled() {
		t.Fatal("motion should default on")
	}
	t.Setenv("JARA_REDUCED_MOTION", "1")
	if chatMotionEnabled() {
		t.Fatal("JARA_REDUCED_MOTION disables motion")
	}
	if blendChatColor("0;0;0", "200;100;50", 0.5) != "100;50;25" {
		t.Fatal("blend")
	}
	shimmer := paintChatShimmer("Thinking", 6, "100;100;100", "250;250;250", "0;0;0", "")
	if !strings.Contains(shimmer, "250;250;250") || !strings.Contains(shimmer, "100;100;100") {
		t.Fatalf("shimmer needs a highlight band and base color: %q", shimmer)
	}
	if textutil.TerminalEscapes.ReplaceAllString(shimmer, "") != "Thinking" {
		t.Fatal("shimmer must not change text")
	}
	palette := menuColorsForBackground(terminalRGB{r: 30, g: 30, b: 30})
	model := chatViewModel{Activity: "Thinking", ActivityElapsed: "3s", Motion: true, Tick: 4, Mark: "✶"}
	frame := renderChatView(model, 80, 20, palette, true)
	joined := strings.Join(frame.Lines, "\n")
	if !strings.Contains(joined, "✶") {
		t.Fatal("the welcome twinkle should render")
	}
	for i, line := range frame.Lines {
		if got := uniseg.StringWidth(textutil.TerminalEscapes.ReplaceAllString(line, "")); got != 80 {
			t.Fatalf("line %d width %d", i, got)
		}
	}
}

func TestChatDiffPaneAddsNewChangesBelowTheShownOnes(t *testing.T) {
	palette := menuColorsForBackground(terminalRGB{r: 30, g: 30, b: 30})
	var d chatDiffState
	d.record([]protocol.FileChange{{Path: "old.go", Diff: "@@ -1 +1 @@\n-a\n+kept\n"}}, false)
	d.record([]protocol.FileChange{{Path: "new.go", Diff: "+added\n"}}, true)
	// While the new file wipes in, the rows already shown stay on screen.
	early := strings.Join(renderChatDiffPane(&d, 50, 20, d.arrived.Add(chatRevealPerRow), true, palette, false), "\n")
	if !strings.Contains(early, "+kept") || strings.Contains(early, "+added") {
		t.Fatalf("only the new change animates:\n%s", early)
	}
	late := strings.Join(renderChatDiffPane(&d, 50, 20, d.arrived.Add(time.Hour), true, palette, false), "\n")
	if strings.Index(late, "old.go") > strings.Index(late, "new.go") || !strings.Contains(late, "+added") {
		t.Fatalf("the newest change goes last:\n%s", late)
	}

	// The pane follows the newest change, or holds still when scrolled back.
	for i := 0; i < 5; i++ {
		d.record([]protocol.FileChange{{Path: fmt.Sprintf("f%d.go", i), Diff: strings.Repeat("+line\n", 10)}}, false)
	}
	if rows := renderChatDiffPane(&d, 50, 20, time.Now(), false, palette, false); !strings.Contains(strings.Join(rows, "\n"), "f4.go") {
		t.Fatalf("the pane shows the newest change:\n%s", strings.Join(rows, "\n"))
	}
	d.scroll = 15
	before := renderChatDiffPane(&d, 50, 20, time.Now(), false, palette, false)
	d.record([]protocol.FileChange{{Path: "f5.go", Diff: "+line\n"}}, true)
	after := renderChatDiffPane(&d, 50, 20, d.arrived.Add(time.Hour), false, palette, false)
	if strings.Join(before[1:], "\n") != strings.Join(after[1:], "\n") {
		t.Fatalf("a scrolled-back pane moved:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

func TestGitChangesKeepTheirRowsAcrossRefreshes(t *testing.T) {
	a, b, c := protocol.FileChange{Path: "a.go", Diff: "+a\n"}, protocol.FileChange{Path: "b.go", Diff: "+b\n"}, protocol.FileChange{Path: "c.go", Diff: "+c\n"}
	// The session lists the newest first; the pane shows it last.
	shown, fresh := chatMergeGitChanges(nil, []protocol.FileChange{b, a})
	if shown[0].Path != "a.go" || shown[1].Path != "b.go" || fresh != 2 {
		t.Fatalf("first list %+v fresh %d", shown, fresh)
	}
	// a.go changes again and c.go is new: both go last; b.go keeps its row.
	a2 := protocol.FileChange{Path: "a.go", Diff: "+a\n+more\n"}
	shown, fresh = chatMergeGitChanges(shown, []protocol.FileChange{c, a2, b})
	if got := shown[0].Path + "," + shown[1].Path + "," + shown[2].Path; got != "b.go,a.go,c.go" || fresh != 2 || shown[1].Diff != a2.Diff {
		t.Fatalf("refreshed list %s fresh %d", got, fresh)
	}
	// A commit removes b.go; the others stay where they were.
	if shown, fresh = chatMergeGitChanges(shown, []protocol.FileChange{c, a2}); len(shown) != 2 || shown[0].Path != "a.go" || fresh != 0 {
		t.Fatalf("after commit %+v fresh %d", shown, fresh)
	}
}
