package jara

import (
	"strings"
	"testing"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

func TestActivityRowAnimatesAFlowerSunOrExplosion(t *testing.T) {
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
			if uniseg.StringWidth(glyph) != 1 || strings.TrimSpace(glyph) == "" {
				t.Fatalf("%s glyph %q must fill one cell", spinner.name, glyph)
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
