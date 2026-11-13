package jara

import (
	"fmt"
	"strings"

	"krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

// The changes pane's title row carries two buttons: [ ask ] starts a question
// about the changes in the input box, and ✕ closes the pane until /diff or F4
// opens it again.

const (
	chatDiffAskButton   = "[ ask ]"
	chatDiffCloseButton = "✕"
	chatDiffAskFiles    = 3 // files named in the question before "and N more"
)

// chatDiffPaneSuffix is the right end of the title row: the scroll position,
// when the pane scrolls, then the buttons.
func chatDiffPaneSuffix(d *chatDiffState, width, height int) string {
	suffix := ""
	if body := len(chatDiffLines(d, width)) - 1; body > height-1 && height > 1 {
		maxScroll := body - (height - 1)
		suffix = fmt.Sprintf(" %d/%d", min(max(0, d.scroll), maxScroll)+1, maxScroll+1)
	}
	if len(d.changes) > 0 {
		suffix += "  " + chatDiffAskButton
	}
	return suffix + "  " + chatDiffCloseButton + " "
}

// chatDiffPaneTargets makes the title's buttons tappable; offset is the
// pane's first column on screen.
func chatDiffPaneTargets(d *chatDiffState, width, height, offset int) []chatTapTarget {
	suffix := chatDiffPaneSuffix(d, width, height)
	start := offset + width - uniseg.StringWidth(suffix)
	var targets []chatTapTarget
	for label, action := range map[string]string{chatDiffAskButton: "diff-ask", chatDiffCloseButton: "diff-close"} {
		index := strings.Index(suffix, label)
		if index < 0 {
			continue
		}
		from := start + uniseg.StringWidth(suffix[:index])
		targets = append(targets, chatTapTarget{row: 0, from: from, to: from + uniseg.StringWidth(label), action: action})
	}
	return targets
}

// togglePane shows or hides the pane where the terminal has room for
// it, and reports whether it could.
func (d *chatDiffState) togglePane() bool {
	if !d.fits {
		return false
	}
	if d.shown {
		d.mode = chatDiffPaneForcedOff
	} else {
		d.mode = chatDiffPaneForcedOn
	}
	return true
}

// chatDiffQuestion starts a question about the changes in the pane, newest
// files first.
func chatDiffQuestion(d *chatDiffState) string {
	var names []string
	seen := map[string]bool{}
	for _, change := range d.changes {
		path := textutil.SanitizeText(textutil.FirstNonEmpty(change.Path, change.NewPath, change.OldPath))
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		names = append(names, path)
	}
	switch {
	case len(names) == 0:
		return ""
	case len(names) > chatDiffAskFiles:
		return fmt.Sprintf("About your changes to %s and %d more files: ", strings.Join(names[:chatDiffAskFiles], ", "), len(names)-chatDiffAskFiles)
	default:
		return "About your changes to " + strings.Join(names, ", ") + ": "
	}
}
