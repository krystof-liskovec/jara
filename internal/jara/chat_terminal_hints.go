//go:build linux || darwin

package jara

import (
	"os"
	"time"

	"golang.org/x/term"
)

// chatTerminalQueue returns the queued prompts' rows and the number of the
// one being edited, or 0.
func chatTerminalQueue(u *chatUI, queueEditor *chatQueueEditor) ([]chatViewLine, int) {
	editNumber := 0
	if queueEditor.active() {
		_, editNumber, _, _ = u.queuedPromptByID(queueEditor.id)
	}
	return u.queueViewLines(queueEditor.id), editNumber
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
