package jara

import (
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/textutil"
	"github.com/rivo/uniseg"
)

type chatViewMessage struct {
	Role, Title, Text, Agent string
	At                       time.Time
}

type chatViewModel struct {
	Workspace, Model, State string
	Messages                []chatViewMessage
	TranscriptLines         []chatViewLine
	Composer                string
	CursorByte              int
	CursorLine              int
	CursorColumn            int
	Suggestions             []chatCompletionChoice
	Activity                string
	ActivityElapsed         string
	ActivityFrame           int
	ActivitySeed            int64  // per-turn seed for the thinking verb
	ActivityHint            string // e.g. "ctrl+c to interrupt"
	QueuedPrompts           []chatViewLine
	QueueEditing            bool
	QueueEditNumber         int
	ControlModel            string
	ControlReasoning        string
	ControlContext          string // context meter, "" when unknown
	ScrollOffset            int
	Hints                   []string
	Motion                  bool // animations enabled
	Tick                    int  // shimmer frame
	Pulse                   float64
	Mark                    string // welcome mark glyph
}

type chatFrame struct {
	Lines                []string
	CursorRow, CursorCol int
}

type chatViewLine struct {
	text, kind, role string
	spans            []chatSyntaxSpan // highlighted code starting at codeStart
	codeStart        int
}

const chatQueueViewRowLimit = 8

// renderChatView builds a fixed-size frame. ScrollOffset counts rows upward
// from the newest transcript row; zero follows the newest content.
func renderChatView(model chatViewModel, width, height int, palette menuPalette, color bool) chatFrame {
	width, height = max(1, width), max(1, height)
	frame := chatFrame{Lines: make([]string, height), CursorRow: -1}
	for i := range frame.Lines {
		frame.Lines[i] = paintChatViewLine(strings.Repeat(" ", width), "canvas", "", false, width, palette, color)
	}
	if height < 9 || width < 18 {
		return renderCompactChatView(model, width, height, palette, color)
	}

	headerRows, footerRows := 2, 1
	controlModel := compactChatModelText(textutil.SanitizeText(model.ControlModel), 24)
	controlReasoning := compactChatModelText(textutil.SanitizeText(model.ControlReasoning), 20)
	contextMeter := ""
	if model.ControlContext != "" {
		contextMeter = "  ◔ Context " + textutil.SanitizeText(model.ControlContext)
	}
	controls := "◈ Model " + controlModel + " (F2)  ◉ Reasoning " + controlReasoning + " (F3)" + contextMeter
	controlLines := []string{controls}
	controlRows := 1
	if height >= 8 && uniseg.StringWidth(" "+controls) > width {
		controlRows = 2
		controlLines = []string{"◈ Model " + controlModel + " (F2)", "◉ Reasoning " + controlReasoning + " (F3)" + contextMeter}
	}
	composerText := sanitizeChatViewText(model.Composer)
	composerLines, cursorLine, cursorColumn := wrapChatComposer(composerText, model.CursorLine, model.CursorColumn, max(1, width-4))
	queueReserve := 0
	if len(model.QueuedPrompts) > 0 {
		queueReserve = 1
	}
	maxInputRows := max(1, min(4, height-headerRows-footerRows-controlRows-2-queueReserve))
	inputRows := max(1, min(maxInputRows, len(composerLines)))
	composerRows := inputRows + 2
	spare := max(0, height-headerRows-footerRows-controlRows-composerRows)
	queueRows := min(min(chatQueueViewRowLimit, len(model.QueuedPrompts)), spare)
	completionRows := min(min(6, len(model.Suggestions)), max(0, spare-queueRows))
	activityRows := 0
	if model.Activity != "" && spare > queueRows+completionRows {
		activityRows = 1
	}
	separatorRows := 0
	if spare-queueRows-completionRows-activityRows > 1 {
		separatorRows = 1
	}
	transcriptRows := max(0, spare-queueRows-completionRows-activityRows-separatorRows)
	transcript := model.TranscriptLines
	if transcript == nil {
		transcript = expandChatTranscript(model.Messages, width-4)
	}
	if len(transcript) == 0 {
		transcript = chatWelcomeTranscript(transcriptRows, width-2, model.Mark)
	}
	visibleTranscript, clippedAbove, clippedBelow := chatTranscriptWindow(transcript, transcriptRows, model.ScrollOffset)

	row := 0
	frame.Lines[row] = pulseChatHeader(paintChatViewLine(chatViewHeader(model, width, false), "header", "", false, width, palette, color), model, palette, color)
	row++
	frame.Lines[row] = paintChatViewLine(chatViewHeader(model, width, true), "subheader", "", false, width, palette, color)
	row++
	for i := 0; i < transcriptRows; i++ {
		line := chatViewLine{kind: "blank"}
		if i < len(visibleTranscript) {
			line = visibleTranscript[i]
		}
		frame.Lines[row] = paintChatTranscriptLine(" ", line, width, palette, color)
		row++
	}
	row += separatorRows
	if activityRows > 0 {
		line := chatActivityViewLine(model)
		frame.Lines[row] = paintChatActivity(chatViewLine{text: " " + fitChatView(line.text, width-2), kind: line.kind}, model, width, palette, color)
		row++
	}
	for _, line := range chatQueueWindow(model.QueuedPrompts, queueRows) {
		frame.Lines[row] = paintChatViewLine(" "+fitChatView(line.text, width-2), line.kind, "", false, width, palette, color)
		row++
	}
	choiceStart := chatCompletionWindow(model.Suggestions, completionRows)
	for i := 0; i < completionRows; i++ {
		choice := model.Suggestions[choiceStart+i]
		text := "  " + textutil.SanitizeText(choice.Value)
		if choice.Description != "" {
			text += "  ·  " + textutil.SanitizeText(choice.Description)
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
	composerLabel, sendLabel := chatComposerLabels(model)
	frame.Lines[row] = paintChatViewLine(chatComposerBorder(width, "╭", "╮", composerLabel, visibleStart > 0), "composer-border", "", false, width, palette, color)
	row++
	innerWidth := max(1, width-4)
	for i := 0; i < inputRows; i++ {
		content := ""
		if i < len(composerParts) {
			content = composerParts[i]
		}
		frame.Lines[row] = paintChatViewLine("  "+fitChatView(content, innerWidth)+"  ", "composer", "", false, width, palette, color)
		row++
	}
	frame.Lines[row] = paintChatViewLine(chatComposerBorder(width, "╰", "╯", sendLabel, visibleStart+inputRows < len(composerLines)), "composer-border", "", false, width, palette, color)
	row++
	for _, control := range controlLines {
		frame.Lines[row] = paintChatViewLine(" "+fitChatView(control, width-2), "controls", "", false, width, palette, color)
		row++
	}
	frame.Lines[height-1] = paintChatViewLine(chatFooter(model, width, clippedAbove, clippedBelow), "footer", "", false, width, palette, color)

	if cursorLine >= visibleStart && cursorLine < visibleStart+inputRows {
		frame.CursorRow = composerStart + 1 + cursorLine - visibleStart
		frame.CursorCol = min(width-1, 2+max(0, cursorColumn))
	}
	return frame
}

func chatViewHeader(model chatViewModel, width int, sub bool) string {
	if !sub {
		workspace := strings.TrimSpace(textutil.SanitizeText(model.Workspace))
		if workspace == "" {
			workspace = "Workspace"
		}
		return "✦ JARA  /  " + workspace
	}
	modelName := strings.TrimSpace(textutil.SanitizeText(model.Model))
	state := strings.TrimSpace(textutil.SanitizeText(model.State))
	if modelName == "" || modelName == "not reported by Codex" {
		modelName = "Codex"
	}
	if state == "" {
		state = "idle"
	}
	return modelName + "   ·   " + state
}

func chatWelcomeTranscript(rows, width int, mark string) []chatViewLine {
	if mark == "" {
		mark = "✦"
	}
	content := []chatViewLine{
		{text: mark, kind: "welcome-mark"},
		{text: "Ready when you are", kind: "welcome-title"},
		{text: "Ask Codex or Claude to explore or change this project.", kind: "welcome-copy"},
		{text: "/resume  recent sessions     /help  command guide", kind: "welcome-hint"},
	}
	top := max(0, (rows-len(content))/2)
	lines := make([]chatViewLine, top, max(rows, top+len(content)))
	for i := range lines {
		lines[i].kind = "welcome-space"
	}
	for _, line := range content {
		line.text = centerChatViewText(line.text, width)
		lines = append(lines, line)
	}
	for len(lines) < rows {
		lines = append(lines, chatViewLine{kind: "welcome-space"})
	}
	return lines
}

func centerChatViewText(text string, width int) string {
	return strings.Repeat(" ", max(0, (width-uniseg.StringWidth(text))/2)) + text
}

func chatFooter(model chatViewModel, width int, clippedAbove, clippedBelow bool) string {
	hints := model.Hints
	if len(hints) == 0 {
		hints = []string{"↑↓ choose", "Tab complete", "Enter send", "Ctrl+C quit"}
	}
	if clippedAbove || clippedBelow {
		if model.QueueEditing {
			hints = []string{"Shift+drag select", "PgUp/Dn/wheel", "Enter save", "Esc cancel"}
		} else if strings.Contains(strings.Join(hints, " "), "Esc close") {
			hints = []string{"↑↓ choose", "Enter open", "Esc close"}
		} else {
			interrupt := "Ctrl+C quit"
			if strings.Contains(strings.Join(hints, " "), "cancel") {
				interrupt = "Ctrl+C cancel"
			}
			hints = []string{"Shift+drag select", "PgUp/Dn/wheel history", "Shift+Enter", "Enter send", interrupt}
		}
		markers := []string{}
		if clippedAbove {
			markers = append(markers, "↑ older")
		}
		if clippedBelow {
			markers = append(markers, "↓ newer")
		}
		hints = append(hints, strings.Join(markers, " "))
	}
	if len(model.QueuedPrompts) > 0 && !model.QueueEditing && !strings.Contains(strings.Join(hints, " "), "Esc close") {
		hints = append(hints, "/queue edit N")
	}
	return strings.Join(hints, "  ")
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
	remaining := max(0, width-unisegStringWidth(left+right+label))
	return left + label + strings.Repeat("─", remaining) + right
}

func unisegStringWidth(text string) int { return uniseg.StringWidth(text) }

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

func chatComposerLabels(model chatViewModel) (string, string) {
	if model.QueueEditing {
		label := " Edit queue "
		if model.QueueEditNumber > 0 {
			label = " Edit #" + strconv.Itoa(model.QueueEditNumber) + " "
		}
		return label, " Enter save "
	}
	return " Message ", " Enter send "
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
