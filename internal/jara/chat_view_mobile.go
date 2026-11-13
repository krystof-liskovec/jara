package jara

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Phones are narrow, and short while the on-screen keyboard is open. Below
// these sizes the view drops chrome and function-key labels, and every
// control can be tapped instead (terminal mouse reporting turns taps into
// clicks).
const (
	chatShortScreenRows = 18
	chatNarrowColumns   = 60
)

// chatTapTarget is a tappable region of one screen row, in cells.
type chatTapTarget struct {
	row, from, to int
	action        string // model, reasoning, approve, deny, diff, or suggestion:N
}

// addTarget makes the first occurrence of segment in a painted row tappable.
func (f *chatFrame) addTarget(row int, line, segment, action string) {
	if segment == "" {
		return
	}
	index := strings.Index(line, segment)
	if index < 0 {
		return
	}
	from := uniseg.StringWidth(line[:index])
	f.Targets = append(f.Targets, chatTapTarget{row: row, from: from, to: from + uniseg.StringWidth(segment), action: action})
}

// target returns the action at a cell, or "".
func (f chatFrame) target(row, column int) string {
	for _, target := range f.Targets {
		if target.row == row && column >= target.from && column < target.to {
			return target.action
		}
	}
	return ""
}

// collapseChatBlankLines keeps at most one blank row in a row and none at
// the end, so short screens show conversation instead of spacing.
func collapseChatBlankLines(lines []chatViewLine) []chatViewLine {
	out := make([]chatViewLine, 0, len(lines))
	for _, line := range lines {
		blank := strings.TrimSpace(line.text) == ""
		if blank && (len(out) == 0 || strings.TrimSpace(out[len(out)-1].text) == "") {
			continue
		}
		out = append(out, line)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1].text) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// chatPhoneHints replaces hints that need a desktop keyboard or mouse.
func chatPhoneHints(hints []string) []string {
	out := []string{}
	for _, hint := range hints {
		if strings.Contains(hint, "Shift+") || strings.Contains(hint, "PgUp") || strings.Contains(hint, "Alt+") || strings.HasPrefix(hint, "F") && len(hint) > 1 && hint[1] >= '0' && hint[1] <= '9' {
			continue
		}
		out = append(out, hint)
	}
	if len(out) > 1 {
		return append(out[:1], append([]string{`\ Enter newline`}, out[1:]...)...)
	}
	return append(out, `\ Enter newline`)
}
