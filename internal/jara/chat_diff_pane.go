package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rivo/uniseg"
	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// The diff pane shows recent file changes, newest last: like the
// conversation, new changes are added at the bottom and the pane follows
// them unless it was scrolled back. On wide terminals
// it sits beside the conversation and opens with the first change. Smaller
// terminals, such as phones, would squeeze the conversation, so there it
// never opens by itself: F4 or /diff shows it over the whole screen. F4,
// /diff and its ✕ button toggle it; Alt+PgUp/PgDn or F5/F6 scroll it.
const (
	chatDiffPaneMinWidth   = 120 // leaves the conversation about 72 columns
	chatDiffPaneMinHeight  = chatShortScreenRows
	chatDiffPaneKeepFiles  = 30
	chatDiffPaneFileLines  = 400
	chatDiffPaneAuto       = 0
	chatDiffPaneForcedOn   = 1
	chatDiffPaneForcedOff  = -1
	chatDiffPaneScrollStep = 10
)

type chatDiffState struct {
	changes []protocol.FileChange // the agent's edits, oldest first
	arrived time.Time
	fresh   int // changes at the end of the list that just arrived and wipe in
	mode    int
	scroll  int // rows up from the newest change; 0 follows new changes
	// The interactive terminal's size, recorded on each draw; zero in plain
	// output, where /diff lists the changes instead.
	screenWidth, screenHeight int
	git                       chatGitDiff // uncommitted changes, in a Git workspace
}

// recordFileChanges keeps the newest changes for the pane. Live changes
// animate in; changes replayed from history appear immediately.
func (d *chatDiffState) recordFileChanges(raw json.RawMessage, live bool) {
	var item struct {
		Changes []protocol.FileChange `json:"changes"`
	}
	if json.Unmarshal(raw, &item) != nil || len(item.Changes) == 0 {
		return
	}
	d.record(item.Changes, live)
}

func (d *chatDiffState) record(changes []protocol.FileChange, live bool) {
	// Newer edits go last; files within one edit keep the agent's order.
	if d.git.available {
		// The pane shows Git's changes, which update on their own.
		d.changes = chatKeepNewestChanges(append(d.changes, changes...))
		return
	}
	d.holdPlace(func() {
		d.changes = chatKeepNewestChanges(append(d.changes, changes...))
		d.arrived, d.fresh = time.Time{}, 0
		if live {
			d.arrived, d.fresh = time.Now(), min(len(changes), len(d.changes))
		}
	})
}

func chatKeepNewestChanges(changes []protocol.FileChange) []protocol.FileChange {
	if over := len(changes) - chatDiffPaneKeepFiles; over > 0 {
		return append([]protocol.FileChange(nil), changes[over:]...)
	}
	return changes
}

// holdPlace applies update to the pane's changes. A pane scrolled back stays
// still while changes are added below it; one following the newest changes
// keeps following them.
func (d *chatDiffState) holdPlace(update func()) {
	if d.scroll == 0 {
		update()
		return
	}
	before := len(chatDiffLines(d, 0))
	update()
	d.scroll = max(0, d.scroll+len(chatDiffLines(d, 0))-before)
}

func (d *chatDiffState) toggle(width, height int) {
	if chatDiffPaneWidth(d, width, height) > 0 || chatDiffFullScreen(d, width, height) {
		d.hide(width, height)
	} else {
		d.mode = chatDiffPaneForcedOn
	}
}

// hide closes the pane. A closed side pane stays closed until reopened; a
// closed full-screen view lets the pane open by itself again should the
// terminal grow wide enough.
func (d *chatDiffState) hide(width, height int) {
	if chatDiffFullScreen(d, width, height) {
		d.mode = chatDiffPaneAuto
	} else {
		d.mode = chatDiffPaneForcedOff
	}
}

// chatDiffSideFits reports whether the pane fits beside the conversation.
func chatDiffSideFits(width, height int) bool {
	return width >= chatDiffPaneMinWidth && height >= chatDiffPaneMinHeight
}

// chatDiffFullScreen reports whether the pane, opened on purpose, covers the
// screen because it does not fit beside the conversation.
func chatDiffFullScreen(d *chatDiffState, width, height int) bool {
	return d != nil && d.mode == chatDiffPaneForcedOn && !chatDiffSideFits(width, height) && width > 0 && height > 0
}

// animating reports whether the reveal or highlight is still in progress.
func (d *chatDiffState) animating(now time.Time) bool {
	return !d.arrived.IsZero() && now.Sub(d.arrived) < chatFlashLength
}

// chatDiffPaneWidth returns the pane width including its separator, or 0
// when the pane is hidden or the terminal is too small.
func chatDiffPaneWidth(d *chatDiffState, width, height int) int {
	if d == nil || d.mode == chatDiffPaneForcedOff || !chatDiffSideFits(width, height) {
		return 0
	}
	if d.mode == chatDiffPaneAuto && len(d.changes) == 0 {
		return 0
	}
	return min(max(width*2/5, 44), 100)
}

type chatDiffLine struct {
	text, kind string
	fresh      bool
	spans      []chatSyntaxSpan // highlighted code after the diff marker
}

func chatDiffLines(d *chatDiffState, width int) []chatDiffLine {
	changes := d.visible()
	added, removed := 0, 0
	for _, change := range changes {
		a, r := chatFileChangeCounts(change.Diff)
		added, removed = added+a, removed+r
	}
	files := fmt.Sprintf("%d files", len(changes))
	if len(changes) == 1 {
		files = "1 file"
	}
	title, empty := "Changes", []string{"No file changes yet.", "Edits from the agent appear here."}
	if d.git.available {
		title, empty = "Uncommitted", []string{"Working tree is clean.", "Uncommitted changes appear here."}
		if d.git.omitted > 0 {
			files += fmt.Sprintf(" (+%d not shown)", d.git.omitted)
		}
	}
	lines := []chatDiffLine{{text: fmt.Sprintf(" %s · %s · +%d −%d", title, files, added, removed), kind: "pane-title"}}
	if len(changes) == 0 {
		return append(lines, chatDiffLine{kind: "pane-blank"}, chatDiffLine{text: "  " + empty[0], kind: "pane-empty"}, chatDiffLine{text: "  " + empty[1], kind: "pane-empty"})
	}
	freshFrom := len(changes) - d.fresh
	for index, change := range changes {
		path := strings.Join(strings.Fields(chatText(textutil.FirstNonEmpty(change.Path, change.NewPath, change.OldPath, "Unknown file"))), " ")
		a, r := chatFileChangeCounts(change.Diff)
		header := fmt.Sprintf(" ● %s", path)
		if kind := chatFileChangeKind(change.Kind); kind != "" && kind != "update" {
			header += " · " + kind
		}
		header += fmt.Sprintf("  +%d −%d", a, r)
		lines = append(lines, chatDiffLine{kind: "pane-blank"}, chatDiffLine{text: header, kind: "pane-file", fresh: index >= freshFrom})
		shown := 0
		for _, raw := range strings.Split(strings.TrimRight(chatText(change.Diff), "\n"), "\n") {
			if raw == "" || strings.HasPrefix(raw, "diff --git") || strings.HasPrefix(raw, "index ") || strings.HasPrefix(raw, "--- ") || strings.HasPrefix(raw, "+++ ") {
				continue
			}
			if shown == chatDiffPaneFileLines {
				lines = append(lines, chatDiffLine{text: "   … more lines omitted", kind: "pane-more"})
				break
			}
			raw = strings.ReplaceAll(raw, "\t", "    ")
			kind := "pane-context"
			switch {
			case strings.HasPrefix(raw, "@@"):
				kind = "pane-hunk"
			case strings.HasPrefix(raw, "+"):
				kind = "pane-add"
			case strings.HasPrefix(raw, "-"):
				kind = "pane-delete"
			}
			_, spans := chatDiffSyntax(path, raw)
			lines = append(lines, chatDiffLine{text: " " + raw, kind: kind, spans: spans})
			shown++
		}
	}
	return lines
}

// renderChatDiffPane paints height rows of the pane, applying the reveal
// wipe and the highlight on the files that just arrived while motion is
// enabled. Rows already shown stay put; only the new ones wipe in.
func renderChatDiffPane(d *chatDiffState, width, height int, now time.Time, motion bool, palette menuPalette, color bool) []string {
	lines := chatDiffLines(d, width)
	body := lines[1:]
	maxScroll := max(0, len(body)-(height-1))
	d.scroll = min(max(0, d.scroll), maxScroll)
	start := maxScroll - d.scroll
	revealed, freshStart := len(body), len(body)
	elapsed := now.Sub(d.arrived)
	if motion && !d.arrived.IsZero() && elapsed < chatFlashLength {
		revealed = int(elapsed / chatRevealPerRow)
		for index, line := range body {
			if line.fresh {
				freshStart = max(0, index-1) // from the blank row above the file
				break
			}
		}
	}
	out := make([]string, height)
	out[0] = paintChatDiffLine(lines[0], width, 0, palette, color)
	for row := 1; row < height; row++ {
		index := start + row - 1
		line := chatDiffLine{kind: "pane-blank"}
		if index < len(body) && (index < freshStart || index-max(freshStart, start) < revealed) {
			line = body[index]
		}
		flash := 0.0
		if line.fresh && motion && !d.arrived.IsZero() && elapsed < chatFlashLength {
			flash = 1 - float64(elapsed)/float64(chatFlashLength)
		}
		out[row] = paintChatDiffLine(line, width, flash, palette, color)
	}
	title := overlayRight(lines[0].text, chatDiffPaneSuffix(d, width, height), width)
	out[0] = paintChatDiffLine(chatDiffLine{text: title, kind: "pane-title"}, width, 0, palette, color)
	return out
}

func overlayRight(text, suffix string, width int) string {
	room := width - uniseg.StringWidth(suffix)
	if room <= 0 {
		return fitChatView(suffix, width)
	}
	text = fitChatView(text, room)
	return text + strings.Repeat(" ", max(0, room-uniseg.StringWidth(text))) + suffix
}

func paintChatDiffLine(line chatDiffLine, width int, flash float64, palette menuPalette, color bool) string {
	text := fitChatView(line.text, width)
	plain := text + strings.Repeat(" ", max(0, width-uniseg.StringWidth(text)))
	if !color {
		return plain
	}
	addBG, delBG, delFG := chatDiffColors(palette)
	bg, fg, style := palette.canvas, palette.text, ""
	switch line.kind {
	case "pane-title":
		bg, fg, style = palette.surface, palette.text, "\x1b[1m"
	case "pane-file":
		bg, fg, style = blendChatColor(palette.canvas, palette.accent, 0.35*flash), palette.text, "\x1b[1m"
	case "pane-add":
		bg, fg = addBG, palette.green
	case "pane-delete":
		bg, fg = delBG, delFG
	case "pane-hunk":
		fg = palette.accent
	case "pane-context", "pane-empty", "pane-more":
		fg = palette.muted
	}
	if len(line.spans) > 0 && line.text != "" {
		// Keep the marker in the diff color and color the code by syntax.
		marker := line.text[:2]
		code := fg
		if line.kind != "pane-context" {
			code = palette.text
		}
		return paintChatSyntax(marker, line.spans, width, bg, fg, code, style, chatColorIsLight(palette.canvas))
	}
	painted := menuBG(bg) + menuFG(fg) + style + plain
	if line.kind == "pane-file" {
		painted = strings.Replace(painted, "●", menuFG(blendChatColor(palette.accent, palette.green, flash))+"●"+menuFG(fg), 1)
	}
	return painted + "\x1b[0m"
}

// composeChatScreen places the diff pane to the right of a rendered chat
// frame, separated by a thin rule.
func composeChatScreen(frame chatFrame, pane []string, palette menuPalette, color bool) chatFrame {
	separator := "│"
	if color {
		separator = menuBG(palette.canvas) + menuFG(blendChatColor(palette.canvas, palette.muted, 0.6)) + "│\x1b[0m"
	}
	for row := range frame.Lines {
		if row < len(pane) {
			frame.Lines[row] += separator + pane[row]
		}
	}
	return frame
}
