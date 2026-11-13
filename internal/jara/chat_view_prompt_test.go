package jara

import (
	"strings"
	"testing"
)

func TestComposerStartsWithPromptMarker(t *testing.T) {
	model := chatViewModel{Composer: "hello\nsecond line", CursorLine: 0, CursorColumn: 5, ControlModel: "gpt", ControlReasoning: "high"}
	frame := renderChatView(model, 40, 20, menuPalette{}, false)
	first, second := -1, -1
	for index, line := range frame.Lines {
		switch {
		case strings.HasPrefix(line, "❯ hello"):
			first = index
		case strings.HasPrefix(line, "  second line"):
			second = index
		}
	}
	if first < 0 || second != first+1 {
		t.Fatalf("composer lines (first=%d second=%d):\n%s", first, second, strings.Join(frame.Lines, "\n"))
	}
	if frame.CursorRow != first || frame.CursorCol != 2+5 {
		t.Fatalf("cursor at %d:%d, want %d:7", frame.CursorRow, frame.CursorCol, first)
	}

	compact := renderChatView(chatViewModel{Composer: "hi", CursorColumn: 2}, 30, 6, menuPalette{}, false)
	if line := compact.Lines[compact.CursorRow]; !strings.HasPrefix(line, "❯ hi") || compact.CursorCol != 4 {
		t.Fatalf("compact composer %q cursor %d", line, compact.CursorCol)
	}
}
