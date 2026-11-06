package jara

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

type chatEditorPastePreview struct {
	start, end, chars int
}

func (e *chatEditor) addPastePreview(start, end int) {
	if start < 0 || end <= start || end > len(e.text) {
		return
	}
	content := e.text[start:end]
	chars := utf8.RuneCountInString(content)
	if chars < 200 && strings.Count(content, "\n") < 3 {
		return
	}
	e.pastePreviews = append(e.pastePreviews, chatEditorPastePreview{start: start, end: end, chars: chars})
	sort.Slice(e.pastePreviews, func(i, j int) bool { return e.pastePreviews[i].start < e.pastePreviews[j].start })
}

func (e *chatEditor) shiftPastePreviewsForInsert(at, size int) {
	shifted := make([]chatEditorPastePreview, 0, len(e.pastePreviews))
	for _, preview := range e.pastePreviews {
		if at <= preview.start {
			preview.start += size
			preview.end += size
		}
		if at >= preview.end || at <= preview.start {
			shifted = append(shifted, preview)
		}
	}
	e.pastePreviews = shifted
}

func (e *chatEditor) removePastePreviewsInRange(start, end int) {
	removed := end - start
	if removed <= 0 {
		return
	}
	kept := e.pastePreviews[:0]
	for _, preview := range e.pastePreviews {
		if end <= preview.start {
			preview.start -= removed
			preview.end -= removed
			kept = append(kept, preview)
		} else if start >= preview.end {
			kept = append(kept, preview)
		}
	}
	e.pastePreviews = kept
}

func (e *chatEditor) snapCursorToPasteBoundary() {
	for _, preview := range e.pastePreviews {
		if e.cursor > preview.start && e.cursor < preview.end {
			if e.cursor-preview.start <= preview.end-e.cursor {
				e.cursor = preview.start
			} else {
				e.cursor = preview.end
			}
			return
		}
	}
}

func (e *chatEditor) previewText() (string, int) {
	if len(e.pastePreviews) == 0 {
		return e.text, e.cursor
	}
	var text strings.Builder
	position, shift, cursor := 0, 0, e.cursor
	cursorSet := false
	for _, preview := range e.pastePreviews {
		if preview.start < position || preview.end > len(e.text) {
			continue
		}
		text.WriteString(e.text[position:preview.start])
		if !cursorSet && e.cursor <= preview.start {
			cursor = e.cursor + shift
			cursorSet = true
		}
		label := fmt.Sprintf("[Pasted Content %d chars;]", preview.chars)
		text.WriteString(label)
		if !cursorSet && e.cursor < preview.end {
			cursor = preview.start + shift
			cursorSet = true
		}
		shift += len(label) - (preview.end - preview.start)
		position = preview.end
	}
	text.WriteString(e.text[position:])
	if !cursorSet {
		cursor = e.cursor + shift
	}
	return text.String(), cursor
}

func chatEditorCursorPosition(text string, cursor int) (int, int) {
	cursor = max(0, min(cursor, len(text)))
	prefix := text[:cursor]
	line := strings.Count(prefix, "\n")
	if split := strings.LastIndexByte(prefix, '\n'); split >= 0 {
		prefix = prefix[split+1:]
	}
	return line, uniseg.StringWidth(prefix)
}
