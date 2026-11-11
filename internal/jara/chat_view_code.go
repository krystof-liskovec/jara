package jara

import (
	"os"
	"strings"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

// Fenced code in the transcript is highlighted by its fence language, and
// file-change diffs (fenced as "diff PATH") by the file's language, with
// added and removed lines tinted as in the changes pane.

const chatCodeGutter = "  │ "

// chatCodeFence returns the code state after a fence line: the fence info
// ("go", "diff app.go", or "text") when a block opens, "" when it closes.
func chatCodeFence(trimmed, code string) string {
	if code != "" {
		return ""
	}
	return textutil.FirstNonEmpty(strings.TrimSpace(strings.TrimPrefix(trimmed, "```")), "text")
}

func chatCodeLines(raw string, width int, role, code string) []chatViewLine {
	language, file, _ := strings.Cut(code, " ")
	kind, marker, spans := "code", "", []chatSyntaxSpan(nil)
	if strings.EqualFold(language, "diff") {
		var diffKind string
		diffKind, spans = chatDiffSyntax(file, raw)
		kind = "code-" + diffKind
		if diffKind == "context" {
			kind = "code"
		}
		if spans != nil {
			marker = raw[:1]
		}
	} else {
		spans = chatSyntaxSpans(language, raw)
	}
	if spans == nil {
		return wrapChatLine(raw, width, chatCodeGutter, chatCodeGutter, kind, role, true)
	}
	available := max(1, width-uniseg.StringWidth(chatCodeGutter)-len(marker))
	rows := wrapChatSyntaxSpans(spans, available)
	lines := make([]chatViewLine, 0, len(rows))
	for index, row := range rows {
		prefix := chatCodeGutter + marker
		if index > 0 {
			prefix = chatCodeGutter + strings.Repeat(" ", len(marker))
		}
		lines = append(lines, chatViewLine{text: prefix + chatSyntaxText(row), kind: kind, role: role, spans: row, codeStart: len(prefix)})
	}
	return lines
}

// paintChatTranscriptLine paints a transcript row; highlighted code keeps
// its gutter and diff marker in the line's color.
func paintChatTranscriptLine(lead string, line chatViewLine, width int, palette menuPalette, color bool) string {
	text := lead + line.text
	if len(line.spans) == 0 || !color || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || line.codeStart > len(line.text) {
		return paintChatViewLine(fitChatView(text, width), line.kind, line.role, false, width, palette, color)
	}
	bg, fg := chatCodeLineColors(line.kind, palette)
	return paintChatSyntax(lead+line.text[:line.codeStart], line.spans, width, bg, fg, palette.text, "", chatColorIsLight(palette.canvas))
}

func chatCodeLineColors(kind string, palette menuPalette) (bg, fg string) {
	addBG, delBG, delFG := chatDiffColors(palette)
	switch kind {
	case "code-add":
		return addBG, palette.green
	case "code-delete":
		return delBG, delFG
	case "code-hunk":
		return palette.canvas, palette.accent
	case "code-meta":
		return palette.canvas, palette.muted
	}
	return palette.canvas, palette.green
}
