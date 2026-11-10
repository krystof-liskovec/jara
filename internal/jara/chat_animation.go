package jara

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rivo/uniseg"
)

// Animation timing. Frames are derived from wall-clock time so every redraw
// path agrees on the current frame without shared counters.
const (
	chatShimmerStep  = 45 * time.Millisecond
	chatShimmerBand  = 6
	chatPulsePeriod  = 1600 * time.Millisecond
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

// chatPulse returns a smooth 0..1 breathing value for the header mark.
func chatPulse(now time.Time) float64 {
	phase := float64(now.UnixMilli()%chatPulsePeriod.Milliseconds()) / float64(chatPulsePeriod.Milliseconds())
	return 0.5 - 0.5*math.Cos(2*math.Pi*phase)
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

// paintChatActivity renders the activity row: the spinner and elapsed time in
// the activity color and the label with a shimmer while motion is enabled.
func paintChatActivity(line chatViewLine, model chatViewModel, width int, palette menuPalette, color bool) string {
	plain := paintChatViewLine(line.text, line.kind, "", false, width, palette, color)
	if !color || !model.Motion || os.Getenv("NO_COLOR") != "" {
		return plain
	}
	text := fitChatView(line.text, width)
	split := strings.LastIndex(text, " · ")
	if split < 0 {
		return plain
	}
	prefix, label := text[:split+3], text[split+3:]
	base := palette.accent
	if line.kind == "activity-working" {
		base = palette.green
	}
	glow := blendChatColor(base, palette.text, 0.85)
	padding := strings.Repeat(" ", max(0, width-uniseg.StringWidth(text)))
	return menuBG(palette.canvas) + menuFG(base) + "\x1b[1m" + prefix +
		paintChatShimmer(label, model.Tick, base, glow, palette.canvas, "\x1b[1m") +
		menuFG(palette.text) + padding + "\x1b[0m"
}

// pulseChatHeader breathes the header mark between the muted and brand colors
// while a turn runs.
func pulseChatHeader(line string, model chatViewModel, palette menuPalette, color bool) string {
	if !color || !model.Motion || model.Pulse <= 0 {
		return line
	}
	glow := blendChatColor(palette.muted, palette.brand, model.Pulse)
	return strings.Replace(line, "✦", menuFG(glow)+"\x1b[1m✦\x1b[22m"+menuFG(palette.text), 1)
}
