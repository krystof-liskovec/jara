package jara

import (
	"fmt"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

func chatInlineDump(spans []chatSyntaxSpan) string {
	var parts []string
	for _, span := range spans {
		parts = append(parts, fmt.Sprintf("%d:%s", span.style, span.text))
	}
	return strings.Join(parts, "|")
}

func TestInlineMarkdownBecomesStyling(t *testing.T) {
	bold, italic, code := chatInlineBold, chatInlineItalic, chatInlineCode
	for _, tc := range []struct {
		text, want string
		styled     bool
	}{
		{"**Order:** newest go last", fmt.Sprintf("%d:Order:|0: newest go last", bold), true},
		{"**`[ ask ]`** still", fmt.Sprintf("%d:[ ask ]|0: still", bold|code), true},
		{"The `internal/jara` tests", fmt.Sprintf("0:The |%d:internal/jara|0: tests", code), true},
		{"use ``a`b`` here", fmt.Sprintf("0:use |%d:a`b|0: here", code), true},
		{"an *italic* word", fmt.Sprintf("0:an |%d:italic|0: word", italic), true},
		{"**a *b* c**", fmt.Sprintf("%d:a |%d:b|%d: c", bold, bold|italic, bold), true},
		{"see [the docs](https://x.dev) now", fmt.Sprintf("0:see |%d:the docs|%d: (https://x.dev)|0: now", chatInlineLink, chatInlineURL), true},
		{`a \*literal\* star`, "0:a *literal* star", true},
		// Left as written: no markdown here.
		{"snake_case_name and 2 * 3 * 4", "0:snake_case_name and 2 * 3 * 4", false},
		{"globs a/*.go and b/*.go", "0:globs a/*.go and b/*.go", false},
		{"an unclosed `tick and **bold", "0:an unclosed `tick and **bold", false},
		{"[ ask ] is a button", "0:[ ask ] is a button", false},
	} {
		spans, styled := chatInlineSpans(tc.text)
		if got := chatInlineDump(spans); got != tc.want || styled != tc.styled {
			t.Errorf("%q: got %s (styled %v), want %s (styled %v)", tc.text, got, styled, tc.want, tc.styled)
		}
	}
}

func TestRepliesWrapWithoutMarkdownMarkers(t *testing.T) {
	text := "- **Git workspaces:** files whose diff didn't change keep their place. Changed or new files move to the bottom and animate, in `chat_git_view.go`."
	lines := expandChatMessage(text, 40, "assistant")
	for i, line := range lines {
		if strings.ContainsAny(line.text, "*`") || uniseg.StringWidth(line.text) > 40 {
			t.Fatalf("row %d %q", i, line.text)
		}
	}
	if lines[0].text != "  • Git workspaces: files whose diff" || lines[0].kind != "bullet" || !strings.HasPrefix(lines[1].text, "    ") {
		t.Fatalf("rows %+v", lines)
	}
	joined := ""
	for _, line := range lines {
		joined += strings.TrimSpace(line.text) + " "
	}
	if !strings.Contains(joined, "in chat_git_view.go.") {
		t.Fatalf("words lost in wrapping: %q", joined)
	}

	// What the user typed, and tool output, keep their characters.
	if user := expandChatMessage("**not bold** `x`", 40, "user"); user[0].text != "  **not bold** `x`" {
		t.Fatalf("user row %q", user[0].text)
	}
	if tool := expandChatMessage("**not bold**", 40, "tool"); tool[0].text != "  **not bold**" {
		t.Fatalf("tool row %q", tool[0].text)
	}
}

func TestInlineStylesPaintInTheRowColors(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	palette := menuColorsForBackground(terminalRGB{r: 30, g: 30, b: 30})
	line := expandChatMessage("**Order:** uses `go test`", 50, "assistant")[0]
	painted := paintChatTranscriptLine(" ", line, 50, palette, true)
	if !strings.Contains(painted, "\x1b[1mOrder:") || !strings.Contains(painted, menuFG(palette.green)+"go test") {
		t.Fatalf("styles missing: %q", painted)
	}
	if plain := textutil.TerminalEscapes.ReplaceAllString(painted, ""); plain != fmt.Sprintf("%-50s", "   Order: uses go test") {
		t.Fatalf("plain row %q", plain)
	}
	// A word wider than the row is cut, keeping its style.
	long := expandChatMessage("`"+strings.Repeat("x", 30)+"`", 12, "assistant")
	if len(long) < 3 || long[1].spans[0].style != chatInlineCode {
		t.Fatalf("long code rows %+v", long)
	}
}
