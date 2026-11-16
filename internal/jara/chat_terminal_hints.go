//go:build linux || darwin

package jara

import (
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// chatTerminalHints chooses footer hints and queue rows for the current
// interaction mode.
func chatTerminalHints(u *chatUI, editor *chatEditor, queueEditor *chatQueueEditor, picker *chatTerminalPicker, sessionPicker bool, suggestions []chatCompletionChoice, paneShown bool) ([]string, []chatViewLine, int) {
	interruptHint := "Ctrl+C stop idle session"
	if u.agentTurnActive() || (u.busy && !u.persistentSession) {
		interruptHint = "Ctrl+C cancel turn"
	}
	// Only hints for what applies right now; the always-available keys
	// (send, newline, scroll, select, Ctrl+C) stay out of the footer.
	hints := []string{}
	if len(suggestions) > 0 {
		hints = append(hints, "Tab accept")
	} else if u.agentTurnActive() && u.threadReady && strings.TrimSpace(editor.Text()) != "" {
		hints = append(hints, "Tab queue")
	}
	if paneShown {
		hints = append(hints, "F4 hide changes", "Alt+PgUp/Dn changes")
	} else if len(u.diff.visible()) > 0 {
		hints = append(hints, "F4 changes")
	}
	queueLines := u.queueViewLines(queueEditor.id)
	queueEditNumber := 0
	if queueEditor.active() {
		_, queueEditNumber, _, _ = u.queuedPromptByID(queueEditor.id)
		hints = []string{"Enter save edit", "Esc cancel"}
	}
	if sessionPicker {
		hints = []string{"↑↓ choose", "Enter open", "Esc close", interruptHint}
	} else if picker.active() {
		hints = []string{"↑↓ choose", "Enter select", "Esc close", "F2 model", "F3 reasoning", interruptHint}
	}
	return hints, queueLines, queueEditNumber
}

func handleChatPaneKey(d *chatDiffState, event chatTerminalKeyEvent, output *os.File) {
	if event.diffPane {
		width, height, err := term.GetSize(int(output.Fd()))
		if err != nil {
			width, height = 80, 24
		}
		d.toggle(width, height)
		return
	}
	d.scroll = max(0, d.scroll-event.paneScroll) // the offset counts up from the newest change
}

// chatAnimationFrame returns the current animation frame, or -1 when nothing
// on screen is animating, so idle sessions do not redraw.
func chatAnimationFrame(u *chatUI, motion bool, now time.Time) int {
	switch {
	case u.busy && motion:
		return int(now.UnixMilli() / chatShimmerStep.Milliseconds())
	case u.busy:
		return int(now.UnixMilli() / 120)
	case motion && u.diff.animating(now):
		return int(now.UnixMilli() / 16)
	case motion:
		return int(now.UnixMilli() / chatTwinklePace.Milliseconds())
	}
	return -1
}

func chatWelcomeMark(motion bool, now time.Time) string {
	if !motion {
		return "✦"
	}
	return chatTwinkleMark(now)
}

// chatTerminalAttachImages turns Ctrl+V and pasted image paths into
// [Image #N] placeholders before the editor sees the input.
func chatTerminalAttachImages(u *chatUI, input *chatEditorInput) {
	switch input.Key {
	case chatEditorClipboardImage:
		input.Key, input.Text = chatEditorText, u.clipboardChatImagePlaceholder()
	case chatEditorPaste:
		if text, ok := u.pastedChatImage(input.Text); ok {
			input.Key, input.Text = chatEditorText, text
		}
	}
}
