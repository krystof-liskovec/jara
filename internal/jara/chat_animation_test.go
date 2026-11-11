package jara

import (
	"strings"
	"testing"
	"time"
)

func TestActivityRowFollowsClaudeCodeSpinner(t *testing.T) {
	var glyphs []string
	for frame := 0; frame < 12; frame++ {
		glyphs = append(glyphs, chatSpinnerGlyph(frame))
	}
	if got := strings.Join(glyphs, ""); got != "·✢✳✶✻✽✻✶✳✢·✢" {
		t.Fatalf("spinner must grow and shrink: %s", got)
	}
	line := chatActivityViewLine(chatViewModel{Activity: "Thinking · ~330 tokens", ActivityElapsed: "12s", ActivityHint: "ctrl+c to interrupt", ActivityFrame: 4})
	if line.text != "✻ Thinking… (12s · ~330 tokens · ctrl+c to interrupt)" || line.kind != "activity" {
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
	if plain := terminalEscapes.ReplaceAllString(painted, ""); !strings.HasPrefix(plain, "· Thinking… (3s · ctrl+c to interrupt)") || len([]rune(plain)) != 60 {
		t.Fatalf("painted row %q", plain)
	}
	if !strings.Contains(painted, menuFG(palette.muted)+" (3s") {
		t.Fatal("details must be muted")
	}
}
