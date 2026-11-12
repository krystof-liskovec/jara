package jara

import (
	"strconv"
	"strings"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

// renderCompactChatView draws terminals too small for the full layout.

func renderCompactChatView(model chatViewModel, width, height int, palette menuPalette, color bool) chatFrame {
	frame := chatFrame{Lines: make([]string, height), CursorRow: -1}
	for row := range frame.Lines {
		frame.Lines[row] = paintChatViewLine(strings.Repeat(" ", width), "canvas", "", false, width, palette, color)
	}
	if height == 0 {
		return frame
	}
	frame.Lines[0] = paintChatViewLine("JARA / "+textutil.SanitizeText(model.Workspace), "header", "", false, width, palette, color)
	composerRows, controlRows := 1, 0
	controlModel := compactChatModelText(textutil.SanitizeText(model.ControlModel), 24)
	controlReasoning := compactChatModelText(textutil.SanitizeText(model.ControlReasoning), 20)
	if height > 1 {
		controlRows = 1
		combined := "M:" + controlModel + " · R:" + controlReasoning + " (F2/F3)"
		if height > 3 && uniseg.StringWidth(combined) > width {
			controlRows = 2
		}
	}
	available := max(0, height-1-composerRows-controlRows)
	queueRows := min(min(chatQueueViewRowLimit, len(model.QueuedPrompts)), available)
	completionRows := min(4, min(len(model.Suggestions), max(0, available-queueRows)))
	activityRows := 0
	if model.Activity != "" && available > queueRows+completionRows {
		activityRows = 1
	}
	separatorRows := 0
	if available-queueRows-completionRows-activityRows > 1 {
		separatorRows = 1
	}
	transcriptRows := max(0, available-queueRows-completionRows-activityRows-separatorRows)
	content := []chatViewLine{}
	if model.Model != "" || model.State != "" {
		content = append(content, chatViewLine{text: textutil.SanitizeText(model.Model) + " · " + textutil.SanitizeText(model.State), kind: "subheader"})
	}
	transcript := model.TranscriptLines
	if transcript == nil {
		transcript = expandChatTranscript(model.Messages, width)
	}
	content = append(content, transcript...)
	if len(transcript) == 0 {
		content = append(content, chatWelcomeTranscript(max(0, transcriptRows-len(content)), width, model.Mark)...)
	}
	visibleHistory, _, _ := chatTranscriptWindow(content, transcriptRows, model.ScrollOffset)
	row := 1
	for index := 0; index < transcriptRows; index++ {
		line := chatViewLine{kind: "blank"}
		if index < len(visibleHistory) {
			line = visibleHistory[index]
		}
		frame.Lines[row] = paintChatTranscriptLine("", line, width, palette, color)
		row++
	}
	row += separatorRows
	if activityRows > 0 {
		line := chatActivityViewLine(model)
		frame.Lines[row] = paintChatActivity(chatViewLine{text: fitChatView(line.text, width), kind: line.kind}, model, width, palette, color)
		row++
	}
	for _, line := range chatQueueWindow(model.QueuedPrompts, queueRows) {
		frame.Lines[row] = paintChatViewLine(fitChatView(line.text, width), line.kind, "", false, width, palette, color)
		row++
	}
	choiceStart := chatCompletionWindow(model.Suggestions, completionRows)
	for index := 0; index < completionRows; index++ {
		choice := model.Suggestions[choiceStart+index]
		text := "  " + textutil.SanitizeText(choice.Value)
		if choice.Description != "" {
			text += "  ·  " + textutil.SanitizeText(choice.Description)
		}
		marker := "  "
		if choice.Selected {
			marker = "› "
		}
		frame.Lines[row] = paintChatViewLine(marker+fitChatView(text, width-2), "completion", "", choice.Selected, width, palette, color)
		row++
	}
	prefix := "> "
	if width < 2 {
		prefix = ">"
	}
	if model.QueueEditing {
		prefix = "Edit> "
	}
	composerWidth := max(1, width-uniseg.StringWidth(prefix))
	composerLines, cursorLine, cursorColumn := wrapChatComposer(model.Composer, model.CursorLine, model.CursorColumn, composerWidth)
	if len(composerLines) == 0 {
		composerLines = []string{""}
	}
	cursorLine = min(max(0, cursorLine), len(composerLines)-1)
	visible, start, leftMore := chatViewWindow(composerLines[cursorLine], max(0, composerWidth), cursorColumn)
	composerRow := height - controlRows - 1
	if controlRows == 1 {
		controls := "M:" + controlModel + " · R:" + controlReasoning + " (F2/F3)"
		frame.Lines[height-1] = paintChatViewLine(fitChatView(controls, width), "controls", "", false, width, palette, color)
	} else if controlRows == 2 {
		frame.Lines[height-2] = paintChatViewLine(fitChatView("Model: "+controlModel+" (F2)", width), "controls", "", false, width, palette, color)
		frame.Lines[height-1] = paintChatViewLine(fitChatView("Reasoning: "+controlReasoning+" (F3)", width), "controls", "", false, width, palette, color)
	}
	frame.Lines[composerRow] = paintChatViewLine(prefix+visible, "composer", "", false, width, palette, color)
	frame.CursorRow = composerRow
	frame.CursorCol = min(width-1, uniseg.StringWidth(prefix)+cursorColumn-start+btoi(leftMore))
	return frame
}

func chatQueueWindow(lines []chatViewLine, rows int) []chatViewLine {
	if rows <= 0 || len(lines) == 0 {
		return nil
	}
	if len(lines) <= rows {
		return lines
	}
	if rows == 1 {
		line := lines[0]
		line.text += "  +" + strconv.Itoa(len(lines)-1) + " more (/queue)"
		return []chatViewLine{line}
	}
	visible := append([]chatViewLine(nil), lines[:rows-1]...)
	visible = append(visible, chatViewLine{
		text: "… " + strconv.Itoa(len(lines)-len(visible)) + " more; /queue to inspect",
		kind: "queued-prompt-more",
	})
	return visible
}

func chatViewWindow(text string, width, cursor int) (string, int, bool) {
	if width <= 0 {
		return "", 0, false
	}
	if uniseg.StringWidth(text) <= width {
		return text, 0, false
	}
	start := max(0, cursor-width+1)
	leftMore := start > 0
	available := width
	if leftMore {
		available--
	}
	var b strings.Builder
	position, used := 0, 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		part, cells := g.Str(), g.Width()
		if position >= start && used+cells <= available {
			b.WriteString(part)
			used += cells
		}
		position += cells
	}
	if leftMore {
		return "‹" + b.String(), start, true
	}
	return b.String(), start, false
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
