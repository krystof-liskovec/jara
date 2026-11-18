package jara

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

// Animation timing. Frames are derived from wall-clock time so every redraw
// path agrees on the current frame without shared counters.
const (
	chatShimmerStep  = 45 * time.Millisecond
	chatShimmerBand  = 6
	chatTwinklePace  = 700 * time.Millisecond
	chatRevealPerRow = 12 * time.Millisecond
	chatFlashLength  = 1500 * time.Millisecond
)

// chatMotionEnabled honors JARA_REDUCED_MOTION and colorless terminals.
func chatMotionEnabled() bool {
	if os.Getenv("JARA_REDUCED_MOTION") == "1" || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return true
}

type chatRGB struct{ r, g, b float64 }

func parseChatRGB(value string) chatRGB {
	parts := strings.Split(value, ";")
	if len(parts) != 3 {
		return chatRGB{}
	}
	channel := func(s string) float64 { v, _ := strconv.Atoi(s); return float64(min(255, max(0, v))) }
	return chatRGB{channel(parts[0]), channel(parts[1]), channel(parts[2])}
}

func (c chatRGB) String() string {
	return fmt.Sprintf("%d;%d;%d", int(math.Round(c.r)), int(math.Round(c.g)), int(math.Round(c.b)))
}

// blendChatColor mixes two "r;g;b" colors; amount 0 is a, 1 is b.
func blendChatColor(a, b string, amount float64) string {
	amount = min(1, max(0, amount))
	x, y := parseChatRGB(a), parseChatRGB(b)
	return chatRGB{x.r + (y.r-x.r)*amount, x.g + (y.g-x.g)*amount, x.b + (y.b-x.b)*amount}.String()
}

func chatColorIsLight(color string) bool {
	c := parseChatRGB(color)
	return 0.2126*c.r+0.7152*c.g+0.0722*c.b > 140
}

// chatDiffColors derives addition and deletion tints from the theme so diffs
// stay legible on light and dark terminals.
func chatDiffColors(palette menuPalette) (addBG, delBG, delFG string) {
	red := "232;92;92"
	if chatColorIsLight(palette.canvas) {
		red = "190;40;40"
	}
	return blendChatColor(palette.canvas, palette.green, 0.2), blendChatColor(palette.canvas, red, 0.22), red
}

func chatTwinkleMark(now time.Time) string {
	marks := []string{"✦", "✧", "✦", "✶", "✷", "✶"}
	return marks[int(now.UnixMilli()/chatTwinklePace.Milliseconds())%len(marks)]
}

// paintChatShimmer colors text with a highlight band sweeping left to right,
// leaving the cell layout unchanged.
func paintChatShimmer(text string, tick int, base, glow, bg, bold string) string {
	cells := uniseg.StringWidth(text)
	cycle := cells + 2*chatShimmerBand
	center := float64(tick%max(1, cycle) - chatShimmerBand)
	var b strings.Builder
	b.WriteString(menuBG(bg) + bold)
	position := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		distance := math.Abs(float64(position) - center)
		intensity := max(0, 1-distance/chatShimmerBand)
		b.WriteString(menuFG(blendChatColor(base, glow, intensity*intensity)) + g.Str())
		position += g.Width()
	}
	return b.String()
}

// The activity row shows an animated glyph, the label with a trailing
// ellipsis under a sweeping shimmer, and muted details in parentheses, e.g.
// "✿ Thinking… (12s · ctrl+c to interrupt)". Each turn picks one animation;
// JARA_SPINNER=flower, sun, explosion, fish, wave or visualizer keeps one. An animation's
// frames are all as wide and have no emoji presentation, so the row never
// shifts.
type chatSpinner struct {
	name   string
	frames []string
	bounce bool // play forwards then backwards instead of restarting
	hold   int  // ticks each frame shows; 0 means one
}

var chatSpinners = []chatSpinner{
	{name: "flower", frames: []string{"·", "∘", "✾", "❀", "✿"}, bounce: true},               // a bud opens and closes
	{name: "sun", frames: []string{"◡", "◒", "●", "☼"}, bounce: true},                       // rises, shines, sets
	{name: "explosion", frames: []string{"·", "•", "✦", "✸", "✹", "✺", "❋", "⁙", "⁘", "∙"}}, // bursts, scatters
	// A fish swims across its pond, turns, and swims back.
	{name: "fish", frames: []string{"><>  ", " ><> ", "  ><>", "  <><", " <>< ", "<><  "}, hold: 2},
	// A wave rolls out from a wall, finds the far one, and rolls back in.
	{name: "wave", frames: []string{"|.  ", "|.. ", "|...", "... ", "...|", "..| ", ".|  "}, hold: 2},
	{name: "visualizer", frames: chatVisualizerFrames()}, // bars dance like an audio visualizer
}

// chatVisualizerFrames draws five bars that rise and fall at their own pace.
// Each bar mixes two waves whose periods divide the loop, so it repeats
// without a jump.
func chatVisualizerFrames() []string {
	const loop = 24
	levels := []rune("▁▂▃▄▅▆▇█")
	bars := []struct{ period, ripple, phase, ripplePhase float64 }{
		{8, 12, 0.0, 1.3}, {6, 24, 2.1, 0.4}, {12, 4, 4.0, 2.2}, {4, 8, 1.1, 3.6}, {24, 6, 5.2, 0.9},
	}
	frames := make([]string, loop)
	for frame := range frames {
		row := make([]rune, len(bars))
		for i, bar := range bars {
			at := 2 * math.Pi * float64(frame)
			height := 0.65*math.Sin(at/bar.period+bar.phase) + 0.35*math.Sin(at/bar.ripple+bar.ripplePhase)
			row[i] = levels[min(len(levels)-1, max(0, int(math.Round((height+1)/2*float64(len(levels)-1)))))]
		}
		frames[frame] = string(row)
	}
	return frames
}

// chatSpinnerFor picks the turn's animation from its seed, unless
// JARA_SPINNER names one.
func chatSpinnerFor(seed int64) chatSpinner {
	if name := strings.TrimSpace(os.Getenv("JARA_SPINNER")); name != "" {
		for _, spinner := range chatSpinners {
			if spinner.name == name {
				return spinner
			}
		}
	}
	// Divide by the verb count so the animation does not follow the verb.
	return chatSpinners[uint64(seed)/uint64(len(chatThinkingVerbs))%uint64(len(chatSpinners))]
}

func (s chatSpinner) glyph(frame int) string {
	frame = max(0, frame) / max(1, s.hold)
	if !s.bounce {
		return s.frames[frame%len(s.frames)]
	}
	cycle := 2*len(s.frames) - 2
	index := frame % cycle
	if index >= len(s.frames) {
		index = cycle - index
	}
	return s.frames[index]
}

func chatActivityViewLine(model chatViewModel) chatViewLine {
	label, detail, _ := strings.Cut(strings.TrimSpace(textutil.SanitizeText(model.Activity)), " · ") // "Thinking · ~N tokens"
	kind := "activity-progress"
	switch label {
	case "Running command", "Applying changes", "Compacting context":
		kind = "activity-working"
	case "Thinking", "Planning", "Waiting for approval", "Waiting for input", "Opening conversation", "Retrying", "Reconnecting", "Stopping":
		kind = "activity"
	}
	if verb := chatThinkingVerb(label, model.ActivitySeed); verb != "" {
		label = verb
	}
	var details []string
	for _, part := range []string{textutil.SanitizeText(model.ActivityElapsed), detail, model.ActivityHint} {
		if part = strings.TrimSpace(part); part != "" {
			details = append(details, part)
		}
	}
	text := chatSpinnerFor(model.ActivitySeed).glyph(model.ActivityFrame) + " " + strings.TrimSuffix(label, "…") + "…"
	if len(details) > 0 {
		text += " (" + strings.Join(details, " · ") + ")"
	}
	return chatViewLine{text: text, kind: kind}
}

// chatThinkingVerbs replace the generic "Thinking" label, one per turn, as
// Claude Code does.
var chatThinkingVerbs = []string{"Thinking", "Pondering", "Cogitating", "Mulling", "Considering", "Deliberating",
	"Reasoning", "Ruminating", "Musing", "Contemplating", "Noodling", "Percolating", "Brewing", "Synthesizing"}

func chatThinkingVerb(label string, seed int64) string {
	if label != "Thinking" || seed == 0 {
		return ""
	}
	return chatThinkingVerbs[uint64(seed)%uint64(len(chatThinkingVerbs))]
}

// paintChatActivity colors the spinner and label in the activity color with
// the shimmer over the label, and the parenthetical details muted.
func paintChatActivity(line chatViewLine, model chatViewModel, width int, palette menuPalette, color bool) string {
	plain := paintChatViewLine(line.text, line.kind, "", false, width, palette, color)
	if !color || os.Getenv("NO_COLOR") != "" {
		return plain
	}
	text := fitChatView(line.text, width)
	glyph, rest, found := strings.Cut(text, " ")
	if !found {
		return plain
	}
	label, details := rest, ""
	if split := strings.LastIndex(rest, " ("); split >= 0 {
		label, details = rest[:split], rest[split:]
	}
	base := palette.accent
	if line.kind == "activity-working" {
		base = palette.green
	}
	painted := menuFG(base) + "\x1b[1m" + label
	if model.Motion {
		painted = paintChatShimmer(label, model.Tick, base, blendChatColor(base, palette.text, 0.85), palette.canvas, "\x1b[1m")
	}
	padding := strings.Repeat(" ", max(0, width-uniseg.StringWidth(text)))
	return menuBG(palette.canvas) + menuFG(base) + "\x1b[1m" + glyph + " " + painted + "\x1b[22m" +
		menuFG(palette.muted) + details + padding + "\x1b[0m"
}
