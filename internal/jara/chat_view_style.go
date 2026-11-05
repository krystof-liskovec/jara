package jara

import (
	"os"
	"strings"

	"github.com/rivo/uniseg"
)

func paintChatViewLine(text, kind, role string, selected bool, width int, palette menuPalette, color bool) string {
	text = fitChatView(text, width)
	padding := strings.Repeat(" ", max(0, width-uniseg.StringWidth(text)))
	plain := text + padding
	if !color || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return plain
	}
	bg, fg, bold := palette.canvas, palette.text, ""
	switch kind {
	case "header":
		bg = palette.surface
	case "subheader":
		bg, fg = palette.surface, palette.muted
	case "footer", "scroll", "empty":
		fg = palette.muted
	case "welcome-space":
		bg = palette.canvas
	case "welcome-mark", "welcome-hint":
		fg = palette.accent
		bold = "\x1b[1m"
	case "welcome-title":
		fg = palette.text
		bold = "\x1b[1m"
	case "welcome-copy":
		fg = palette.muted
	case "activity":
		fg, bold = palette.accent, "\x1b[1m"
	case "controls":
		bg, fg = palette.surface, palette.muted
	case "role-user":
		bg, fg, bold = palette.surface, palette.green, "\x1b[1m"
	case "role-assistant", "heading", "bullet":
		bg, fg = palette.surface, palette.accent
		if kind != "bullet" {
			bold = "\x1b[1m"
		}
	case "role-tool", "role-system":
		bg, fg = palette.surface, palette.muted
	case "code":
		fg = palette.green
	case "completion":
		bg = palette.surface
		if selected {
			bg, fg, bold = palette.selection, menuSelectionText(palette.selection), "\x1b[1m"
		}
	case "composer", "composer-border":
		bg = palette.surface
		if kind == "composer-border" {
			fg, bold = palette.accent, "\x1b[1m"
		}
	}
	line := menuBG(bg) + menuFG(fg) + bold + plain
	if kind == "header" {
		line = strings.Replace(line, "JARA", menuFG(palette.brand)+"\x1b[1mJARA"+menuFG(fg)+bold, 1)
	}
	if kind == "subheader" {
		if split := strings.LastIndex(text, " · "); split >= 0 {
			state := strings.TrimSpace(text[split+3:])
			stateColor := palette.muted
			switch state {
			case "running", "idle":
				stateColor = palette.green
			case "waiting", "starting", "resuming":
				stateColor = palette.accent
			}
			statePaddedStart := split + 3
			statePaddedEnd := statePaddedStart + uniseg.StringWidth(state)
			plainRun := menuBG(bg) + menuFG(stateColor) + "\x1b[1m" + substringCells(plain, statePaddedStart, statePaddedEnd) + menuBG(bg) + menuFG(fg) + bold
			line = menuBG(bg) + menuFG(fg) + bold + substringCells(plain, 0, statePaddedStart) + plainRun + substringCells(plain, statePaddedEnd, width)
		}
	}
	return line + "\x1b[0m"
}

func substringCells(text string, start, end int) string {
	start, end = max(0, start), max(0, end)
	var b strings.Builder
	position := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		part, cells := g.Str(), g.Width()
		if position >= start && position+cells <= end {
			b.WriteString(part)
		}
		position += cells
	}
	return b.String()
}
