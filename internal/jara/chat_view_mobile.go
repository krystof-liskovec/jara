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
// addTargetAfter makes segment tappable where it first appears after the
// text after, so a label that repeats earlier on the row is not confused.
func (f *chatFrame) addTargetAfter(row int, line, after, segment, action string) {
	index := strings.Index(line, after)
	if index < 0 || segment == "" {
		return
	}
	start := index + len(after)
	found := strings.Index(line[start:], segment)
	if found < 0 {
		return
	}
	from := uniseg.StringWidth(line[:start+found])
	f.Targets = append(f.Targets, chatTapTarget{row: row, from: from, to: from + uniseg.StringWidth(segment), action: action})
}

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
