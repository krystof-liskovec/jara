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
	case "message-gap":
		bg = palette.canvas
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
	case "activity", "approval":
		fg, bold = palette.accent, "\x1b[1m"
	case "activity-working":
		fg, bold = palette.green, "\x1b[1m"
	case "activity-progress":
		fg = menuSelectionText(palette.canvas)
	case "controls":
		fg = palette.muted
	case "role-user":
		bg, fg, bold = palette.canvas, palette.green, "\x1b[1m"
	case "role-assistant", "heading", "bullet":
		bg, fg = palette.canvas, palette.accent
		if kind != "bullet" {
			bold = "\x1b[1m"
		}
	case "role-progress":
		bg, fg = palette.canvas, menuSelectionText(palette.canvas)
	case "role-subagent":
		bg, fg = palette.canvas, palette.accent
	case "role-tool", "role-system":
		bg, fg = palette.canvas, palette.muted
	case "body":
		switch role {
		case "user":
			bg = palette.canvas
		case "tool":
			bg, fg = palette.canvas, palette.muted
		case "progress":
			bg, fg, bold = palette.canvas, menuSelectionText(palette.canvas), "\x1b[3m"
		case "subagent", "btw":
			bg, fg = palette.canvas, palette.text
		}
	case "code":
		fg = palette.green
	case "code-add", "code-delete", "code-hunk", "code-meta":
		bg, fg = chatCodeLineColors(kind, palette)
	case "completion":
		if selected {
			bg, fg, bold = palette.selection, menuSelectionText(palette.selection), "\x1b[1m"
		}
	case "queued-prompt":
		fg = palette.text
	case "queued-prompt-edit":
		fg, bold = palette.green, "\x1b[1m"
	case "queued-prompt-more":
		fg = palette.muted
	case "composer", "composer-border":
		// The composer sits on the canvas; its accent border sets it apart.
		if kind == "composer-border" {
			fg, bold = palette.accent, "\x1b[1m"
		}
	}
	line := menuBG(bg) + menuFG(fg) + bold + plain
	return line + "\x1b[0m"
}
