package jara

import (
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

func TestActivityRowPicksAnAnimation(t *testing.T) {
	play := func(spinner chatSpinner, frames int) string {
		var glyphs []string
		for frame := 0; frame < frames; frame++ {
			glyphs = append(glyphs, spinner.glyph(frame))
		}
		return strings.Join(glyphs, "")
	}
	seen := map[string]bool{}
	for seed := int64(0); seed < 200; seed++ {
		seen[chatSpinnerFor(seed).name] = true
	}
	if len(seen) != len(chatSpinners) {
		t.Fatalf("turns pick only %v", seen)
	}
	for _, spinner := range chatSpinners {
		t.Setenv("JARA_SPINNER", spinner.name)
		if got := chatSpinnerFor(7); got.name != spinner.name {
			t.Fatalf("JARA_SPINNER=%s picked %s", spinner.name, got.name)
		}
		for _, glyph := range spinner.frames {
			if uniseg.StringWidth(glyph) != uniseg.StringWidth(spinner.frames[0]) || strings.TrimSpace(glyph) == "" {
				t.Fatalf("%s frame %q must be as wide as the others and show something", spinner.name, glyph)
			}
		}
	}
	t.Setenv("JARA_SPINNER", "flower")
	if got := play(chatSpinnerFor(0), 10); got != "·∘✾❀✿❀✾∘·∘" {
		t.Fatalf("the flower must open and close: %s", got)
	}
	if got := play(chatSpinners[2], 11); got != "·•✦✸✹✺❋⁙⁘∙·" {
		t.Fatalf("the explosion must burst and restart: %s", got)
	}
	// The fish holds each step for two ticks, turns at the far side, and
	// swims back to start again.
	t.Setenv("JARA_SPINNER", "fish")
	var swim []string
	for frame := 0; frame < 14; frame += 2 {
		swim = append(swim, chatSpinnerFor(3).glyph(frame))
	}
	if got := strings.Join(swim, "|"); got != "><>  | ><> |  ><>|  <><| <>< |<><  |><>  " || chatSpinnerFor(3).glyph(1) != "><>  " {
		t.Fatalf("the fish must swim there and back: %s", got)
	}
	if line := chatActivityViewLine(chatViewModel{Activity: "Thinking", ActivityFrame: 4}); !strings.HasPrefix(line.text, "  ><> ") {
		t.Fatalf("fish row %q", line.text)
	}
	t.Setenv("JARA_SPINNER", "wave")
	var wave []string
	for frame := 0; frame < 16; frame += 2 {
		wave = append(wave, chatSpinnerFor(3).glyph(frame))
	}
	if got := strings.Join(wave, "|"); got != "|.  ||.. ||...|... |...||..| |.|  ||.  " {
		t.Fatalf("the wave must roll out and back: %s", got)
	}
	// The visualizer's five bars keep moving: no two frames in a row match,
	// and the last frame leads back into the first.
	frames := chatVisualizerFrames()
	for i, bars := range frames {
		if len([]rune(bars)) != 5 || strings.Trim(bars, "▁▂▃▄▅▆▇█") != "" || bars == frames[(i+1)%len(frames)] {
			t.Fatalf("visualizer frame %d %q (next %q)", i, bars, frames[(i+1)%len(frames)])
		}
	}
	t.Setenv("JARA_SPINNER", "flower")
	line := chatActivityViewLine(chatViewModel{Activity: "Thinking · ~330 tokens", ActivityElapsed: "12s", ActivityHint: "ctrl+c to interrupt", ActivityFrame: 4})
	if line.text != "✿ Thinking… (12s · ~330 tokens · ctrl+c to interrupt)" || line.kind != "activity" {
		t.Fatalf("activity row %q (%s)", line.text, line.kind)
	}
	line = chatActivityViewLine(chatViewModel{Activity: "Thinking", ActivitySeed: 1})
	if !strings.HasSuffix(line.text, chatThinkingVerbs[1]+"…") {
		t.Fatalf("a turn picks its thinking verb: %q", line.text)
	}
	if line = chatActivityViewLine(chatViewModel{Activity: "Running command", ActivitySeed: 1}); line.text != "· Running command…" || line.kind != "activity-working" {
		t.Fatalf("specific activities keep their label: %q", line.text)
	}
	for d, want := range map[time.Duration]string{12 * time.Second: "12s", 184 * time.Second: "3m 4s", 3720 * time.Second: "1h 2m"} {
		if got := chatElapsedLabel(d); got != want {
			t.Errorf("%s: got %s, want %s", d, got, want)
		}
	}
	palette := menuColorsForBackground(terminalRGB{r: 30, g: 30, b: 30})
	painted := paintChatActivity(chatActivityViewLine(chatViewModel{Activity: "Thinking", ActivityElapsed: "3s", ActivityHint: "ctrl+c to interrupt"}),
		chatViewModel{Motion: true, Tick: 3}, 60, palette, true)
	if plain := textutil.TerminalEscapes.ReplaceAllString(painted, ""); !strings.HasPrefix(plain, "· Thinking… (3s · ctrl+c to interrupt)") || len([]rune(plain)) != 60 {
		t.Fatalf("painted row %q", plain)
	}
	if !strings.Contains(painted, menuFG(palette.muted)+" (3s") {
		t.Fatal("details must be muted")
	}
}
