package jara

import (
	"encoding/json"
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
	if got := []string{d.changes[0].Path, d.changes[1].Path, d.changes[2].Path}; strings.Join(got, ",") != "c.go,a.go,b.go" {
		t.Fatalf("order %v", got)
	}
	if chatDiffPaneWidth(&d, 99, 40) != 0 || chatDiffPaneWidth(&d, 160, 11) != 0 {
		t.Fatal("pane must hide on small terminals")
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
	base := time.UnixMilli(0)
	if p := chatPulse(base); p != 0 {
		t.Fatalf("pulse start %f", p)
	}
	if p := chatPulse(base.Add(chatPulsePeriod / 2)); p < 0.99 {
		t.Fatalf("pulse peak %f", p)
	}
	palette := menuColorsForBackground(terminalRGB{r: 30, g: 30, b: 30})
	model := chatViewModel{Workspace: "w", Activity: "Thinking", ActivityElapsed: "3s", Motion: true, Tick: 4, Pulse: 1, Mark: "✶"}
	frame := renderChatView(model, 80, 20, palette, true)
	joined := strings.Join(frame.Lines, "\n")
	if !strings.Contains(joined, "✶") || !strings.Contains(joined, menuFG(palette.brand)+"\x1b[1m✦") {
		t.Fatal("welcome twinkle and header pulse should render")
	}
	for i, line := range frame.Lines {
		if got := uniseg.StringWidth(textutil.TerminalEscapes.ReplaceAllString(line, "")); got != 80 {
			t.Fatalf("line %d width %d", i, got)
		}
	}
}
