package jara

import (
	"fmt"
	"strings"

	"github.com/rivo/uniseg"
	"krystof.liskovec/jara/internal/textutil"
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
		suffix = fmt.Sprintf(" %d/%d", maxScroll-min(max(0, d.scroll), maxScroll)+1, maxScroll+1)
	}
	if len(d.visible()) > 0 {
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

// togglePane shows or hides the pane in the interactive terminal, and
// reports whether there is one.
func (d *chatDiffState) togglePane() bool {
	if d.screenWidth == 0 {
		return false
	}
	d.toggle(d.screenWidth, d.screenHeight)
	return true
}

// hideOnScreen closes the pane as drawn on the current screen.
func (d *chatDiffState) hideOnScreen() { d.hide(d.screenWidth, d.screenHeight) }

func (d *chatDiffState) fullScreen() bool {
	return chatDiffFullScreen(d, d.screenWidth, d.screenHeight)
}

// chatDiffQuestion starts a question about the changes in the pane, newest
// files first.
func chatDiffQuestion(d *chatDiffState) string {
	var names []string
	seen := map[string]bool{}
	changes := d.visible()
	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		path := textutil.SanitizeText(textutil.FirstNonEmpty(change.Path, change.NewPath, change.OldPath))
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		names = append(names, path)
	}
	lead := "About your changes to "
	if d.git.available {
		lead = "About the uncommitted changes to "
	}
	switch {
	case len(names) == 0:
		return ""
	case len(names) > chatDiffAskFiles:
		return fmt.Sprintf("%s%s and %d more files: ", lead, strings.Join(names[:chatDiffAskFiles], ", "), len(names)-chatDiffAskFiles)
	default:
		return lead + strings.Join(names, ", ") + ": "
	}
}
