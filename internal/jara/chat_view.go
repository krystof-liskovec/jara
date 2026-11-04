package jara

import (
	"strings"

	"github.com/rivo/uniseg"
)

type chatViewMessage struct {
	Role, Title, Text string
}

type chatViewModel struct {
	Workspace, Model, State string
	Messages                []chatViewMessage
	Composer                string
	CursorByte              int
	CursorLine              int
	CursorColumn            int
	Suggestions             []chatCompletionChoice
	ScrollOffset            int
	Hints                   []string
}

type chatFrame struct {
	Lines                []string
	CursorRow, CursorCol int
}

type chatViewLine struct {
	text, kind, role string
	selected         bool
}

// renderChatView builds a fixed-size frame. ScrollOffset counts rows upward
// from the newest transcript row; zero follows the newest content.
func renderChatView(model chatViewModel, width, height int, palette menuPalette, color bool) chatFrame {
	width, height = max(1, width), max(1, height)
	frame := chatFrame{Lines: make([]string, height), CursorRow: -1}
	for i := range frame.Lines {
		frame.Lines[i] = paintChatViewLine(strings.Repeat(" ", width), "canvas", "", false, width, palette, color)
	}
	if height < 7 || width < 18 {
		return renderCompactChatView(model, width, height, palette, color)
	}

	headerRows, footerRows := 2, 1
	composerText := sanitizeChatViewText(model.Composer)
	composerLines, cursorLine, cursorColumn := wrapChatComposer(composerText, model.CursorLine, model.CursorColumn, max(1, width-4))
	maxInputRows := max(1, min(4, height-headerRows-footerRows-3))
	inputRows := max(1, min(maxInputRows, len(composerLines)))
	composerRows := inputRows + 2
	spare := max(0, height-headerRows-footerRows-composerRows-1)
	completionRows := min(min(6, len(model.Suggestions)), spare)
	transcriptRows := max(1, height-headerRows-footerRows-composerRows-completionRows)
	transcript := expandChatTranscript(model.Messages, width-4)
	if len(transcript) == 0 {
		transcript = []chatViewLine{{text: "Start a conversation or type /help for commands.", kind: "empty"}}
	}
	if len(model.Suggestions) > 0 && completionRows == 0 {
		transcriptRows = max(1, height-headerRows-footerRows-composerRows)
	}
	visibleTranscript, clippedAbove, clippedBelow := chatTranscriptWindow(transcript, transcriptRows, model.ScrollOffset)

	row := 0
	frame.Lines[row] = paintChatViewLine(chatViewHeader(model, width, false), "header", "", false, width, palette, color)
	row++
	frame.Lines[row] = paintChatViewLine(chatViewHeader(model, width, true), "subheader", "", false, width, palette, color)
	row++
	for i := 0; i < transcriptRows; i++ {
		line := chatViewLine{kind: "blank"}
		if i < len(visibleTranscript) {
			line = visibleTranscript[i]
		}
		frame.Lines[row] = paintChatViewLine(" "+fitChatView(line.text, width-2), line.kind, line.role, false, width, palette, color)
		row++
	}
	choiceStart := chatCompletionWindow(model.Suggestions, completionRows)
	for i := 0; i < completionRows; i++ {
		choice := model.Suggestions[choiceStart+i]
		text := "  " + menuText(choice.Value)
		if choice.Description != "" {
			text += "  ·  " + menuText(choice.Description)
		}
		marker := "  "
		if choice.Selected {
			marker = "› "
		}
		frame.Lines[row] = paintChatViewLine(marker+fitChatView(text, width-4), "completion", "", choice.Selected, width, palette, color)
		row++
	}
	composerStart := row
	composerParts, visibleStart := chatComposerWindow(composerLines, cursorLine, inputRows)
	frame.Lines[row] = paintChatViewLine(chatComposerBorder(width, "╭", "╮", " Message ", visibleStart > 0), "composer-border", "", false, width, palette, color)
	row++
	innerWidth := max(1, width-4)
	for i := 0; i < inputRows; i++ {
		content := ""
		if i < len(composerParts) {
			content = composerParts[i]
		}
		frame.Lines[row] = paintChatViewLine("│ "+fitChatView(content, innerWidth)+" │", "composer", "", false, width, palette, color)
		row++
	}
	frame.Lines[row] = paintChatViewLine(chatComposerBorder(width, "╰", "╯", " Enter send ", visibleStart+inputRows < len(composerLines)), "composer-border", "", false, width, palette, color)
	row++
	frame.Lines[height-1] = paintChatViewLine(chatFooter(model, width, clippedAbove, clippedBelow), "footer", "", false, width, palette, color)

	if cursorLine >= visibleStart && cursorLine < visibleStart+inputRows {
		frame.CursorRow = composerStart + 1 + cursorLine - visibleStart
		frame.CursorCol = min(width-2, 2+max(0, cursorColumn))
	}
	return frame
}

func chatViewHeader(model chatViewModel, width int, sub bool) string {
	if !sub {
		workspace := strings.TrimSpace(menuText(model.Workspace))
		if workspace == "" {
			workspace = "Workspace"
		}
		return "JARA  /  " + workspace
	}
	modelName := strings.TrimSpace(menuText(model.Model))
	state := strings.TrimSpace(menuText(model.State))
	if modelName == "" {
		modelName = "Codex"
	}
	if state == "" {
		state = "idle"
	}
	return "" + modelName + "   ·   " + state
}

func chatFooter(model chatViewModel, width int, clippedAbove, clippedBelow bool) string {
	hints := model.Hints
	if len(hints) == 0 {
		hints = []string{"PgUp/PgDn history", "Tab complete", "Enter send", "Ctrl+C quit"}
	}
	if clippedAbove || clippedBelow {
		markers := []string{}
		if clippedAbove {
			markers = append(markers, "↑ older")
		}
		if clippedBelow {
			markers = append(markers, "↓ newer")
		}
		hints = append(hints, strings.Join(markers, " "))
	}
	return strings.Join(hints, "  ·  ")
}

func chatCompletionWindow(choices []chatCompletionChoice, rows int) int {
	if rows <= 0 || len(choices) <= rows {
		return 0
	}
	selected := -1
	for i, choice := range choices {
		if choice.Selected {
			selected = i
			break
		}
	}
	if selected < 0 {
		return 0
	}
	start := max(0, selected-rows/2)
	return min(start, len(choices)-rows)
}

func chatComposerBorder(width int, left, right, label string, more bool) string {
	marker := ""
	if more {
		marker = " ↑"
	}
	label = label + marker + " "
	remaining := max(0, width-2-unisegStringWidth(left+right+label))
	return left + label + strings.Repeat("─", remaining) + right
}

func chatTranscriptWindow(lines []chatViewLine, height, offset int) ([]chatViewLine, bool, bool) {
	if height <= 0 || len(lines) == 0 {
		return nil, false, false
	}
	maxStart := max(0, len(lines)-height)
	start := max(0, maxStart-max(0, offset))
	end := min(len(lines), start+height)
	return append([]chatViewLine(nil), lines[start:end]...), start > 0, end < len(lines)
}

func chatComposerWindow(lines []string, cursor, height int) ([]string, int) {
	if height < 1 {
		return nil, 0
	}
	start := max(0, cursor-height+1)
	if start > max(0, len(lines)-height) {
		start = max(0, len(lines)-height)
	}
	end := min(len(lines), start+height)
	return append([]string(nil), lines[start:end]...), start
}

func renderCompactChatView(model chatViewModel, width, height int, palette menuPalette, color bool) chatFrame {
	frame := chatFrame{Lines: make([]string, height), CursorRow: -1}
	content := []chatViewLine{{text: "JARA / " + menuText(model.Workspace), kind: "header"}}
	if model.Model != "" || model.State != "" {
		content = append(content, chatViewLine{text: menuText(model.Model) + " · " + menuText(model.State), kind: "subheader"})
	}
	transcript := expandChatTranscript(model.Messages, width)
	content = append(content, transcript...)
	if len(content) == 0 {
		content = []chatViewLine{{text: "JARA", kind: "header"}}
	}
	start := max(0, len(content)-height)
	for row := 0; row < height; row++ {
		line := chatViewLine{kind: "blank"}
		if start+row < len(content) {
			line = content[start+row]
		}
		frame.Lines[row] = paintChatViewLine(fitChatView(line.text, width), line.kind, line.role, false, width, palette, color)
	}
	return frame
}
