package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rivo/uniseg"
)

// The diff pane shows recent file changes beside the conversation on wide
// terminals, newest first. F4 toggles it; Alt+PgUp/PgDn or F5/F6 scroll it.
const (
	chatDiffPaneMinWidth   = 100
	chatDiffPaneMinHeight  = 12
	chatDiffPaneKeepFiles  = 30
	chatDiffPaneFileLines  = 400
	chatDiffPaneAuto       = 0
	chatDiffPaneForcedOn   = 1
	chatDiffPaneForcedOff  = -1
	chatDiffPaneScrollStep = 10
)

type chatDiffState struct {
	changes []chatFileChange
	arrived time.Time
	mode    int
	scroll  int
}

// recordFileChanges keeps the newest changes for the pane. Live changes
// animate in; changes replayed from history appear immediately.
func (d *chatDiffState) recordFileChanges(raw json.RawMessage, live bool) {
	var item struct {
		Changes []chatFileChange `json:"changes"`
	}
	if json.Unmarshal(raw, &item) != nil || len(item.Changes) == 0 {
		return
	}
	d.record(item.Changes, live)
}

func (d *chatDiffState) record(changes []chatFileChange, live bool) {
	// Newer edits go first; files within one edit keep the agent's order.
	d.changes = append(append([]chatFileChange{}, changes...), d.changes...)
	if len(d.changes) > chatDiffPaneKeepFiles {
		d.changes = d.changes[:chatDiffPaneKeepFiles]
	}
	d.scroll = 0
	if live {
		d.arrived = time.Now()
	} else {
		d.arrived = time.Time{}
	}
}

func (d *chatDiffState) toggle(width, height int) {
	if chatDiffPaneWidth(d, width, height) > 0 {
		d.mode = chatDiffPaneForcedOff
	} else {
		d.mode = chatDiffPaneForcedOn
	}
}

// animating reports whether the reveal or highlight is still in progress.
func (d *chatDiffState) animating(now time.Time) bool {
	return !d.arrived.IsZero() && now.Sub(d.arrived) < chatFlashLength
}

// chatDiffPaneWidth returns the pane width including its separator, or 0
// when the pane is hidden or the terminal is too small.
func chatDiffPaneWidth(d *chatDiffState, width, height int) int {
	if d == nil || d.mode == chatDiffPaneForcedOff || width < chatDiffPaneMinWidth || height < chatDiffPaneMinHeight {
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
	added, removed := 0, 0
	for _, change := range d.changes {
		a, r := chatFileChangeCounts(change.Diff)
		added, removed = added+a, removed+r
	}
	files := fmt.Sprintf("%d files", len(d.changes))
	if len(d.changes) == 1 {
		files = "1 file"
	}
	lines := []chatDiffLine{{text: fmt.Sprintf(" Changes · %s · +%d −%d", files, added, removed), kind: "pane-title"}}
	if len(d.changes) == 0 {
		return append(lines, chatDiffLine{kind: "pane-blank"}, chatDiffLine{text: "  No file changes yet.", kind: "pane-empty"}, chatDiffLine{text: "  Edits from the agent appear here.", kind: "pane-empty"})
	}
	for index, change := range d.changes {
		path := strings.Join(strings.Fields(chatText(firstNonEmpty(change.Path, change.NewPath, change.OldPath, "Unknown file"))), " ")
		a, r := chatFileChangeCounts(change.Diff)
		header := fmt.Sprintf(" ● %s", path)
		if kind := chatFileChangeKind(change.Kind); kind != "" && kind != "update" {
			header += " · " + kind
		}
		header += fmt.Sprintf("  +%d −%d", a, r)
		lines = append(lines, chatDiffLine{kind: "pane-blank"}, chatDiffLine{text: header, kind: "pane-file", fresh: index == 0})
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// renderChatDiffPane paints height rows of the pane, applying the reveal
// wipe and the highlight on the newest file while motion is enabled.
func renderChatDiffPane(d *chatDiffState, width, height int, now time.Time, motion bool, palette menuPalette, color bool) []string {
	lines := chatDiffLines(d, width)
	body := lines[1:]
	maxScroll := max(0, len(body)-(height-1))
	d.scroll = min(max(0, d.scroll), maxScroll)
	revealed := len(body)
	elapsed := now.Sub(d.arrived)
	if motion && !d.arrived.IsZero() && elapsed < chatFlashLength {
		revealed = int(elapsed / chatRevealPerRow)
	}
	out := make([]string, height)
	out[0] = paintChatDiffLine(lines[0], width, 0, palette, color)
	for row := 1; row < height; row++ {
		index := d.scroll + row - 1
		line := chatDiffLine{kind: "pane-blank"}
		if index < len(body) && index-d.scroll < revealed {
			line = body[index]
		}
		flash := 0.0
		if line.fresh && motion && !d.arrived.IsZero() && elapsed < chatFlashLength {
			flash = 1 - float64(elapsed)/float64(chatFlashLength)
		}
		out[row] = paintChatDiffLine(line, width, flash, palette, color)
	}
	if maxScroll > 0 && height > 1 {
		marker := fmt.Sprintf(" %d/%d ", d.scroll+1, maxScroll+1)
		out[0] = paintChatDiffLine(chatDiffLine{text: overlayRight(lines[0].text, marker, width), kind: "pane-title"}, width, 0, palette, color)
	}
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
