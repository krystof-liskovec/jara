package jara

import (
	"strconv"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
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
	ApprovalPending         bool   // show the Allow/Deny row
	ChangedFiles            int    // files changed in this conversation, for the header
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
	Targets              []chatTapTarget // tappable regions, for phones
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

	// Phones: with the keyboard open the screen is short, so the chrome
	// shrinks to one header row and the controls move into the input border.
	short, narrow := height < chatShortScreenRows, width < chatNarrowColumns
	headerRows, footerRows := 2, 1
	if short {
		headerRows, footerRows = 1, 0
	}
	keyLabel := func(key string) string {
		if narrow {
			return "" // phone keyboards have no function keys; tap instead
		}
		return " (" + key + ")"
	}
	controlModel := compactChatModelText(textutil.SanitizeText(model.ControlModel), 24)
	controlReasoning := compactChatModelText(textutil.SanitizeText(model.ControlReasoning), 20)
	contextMeter := ""
	if model.ControlContext != "" {
		contextMeter = "  ◔ Context " + textutil.SanitizeText(model.ControlContext)
	}
	controls := "◈ Model " + controlModel + keyLabel("F2") + "  ◉ Reasoning " + controlReasoning + keyLabel("F3") + contextMeter
	controlLines := []string{controls}
	if uniseg.StringWidth(" "+controls) > width {
		controlLines = []string{"◈ Model " + controlModel + keyLabel("F2"), "◉ Reasoning " + controlReasoning + keyLabel("F3") + contextMeter}
	}
	if short {
		controlLines = nil
	}
	controlRows := len(controlLines)
	approvalRows := btoi(model.ApprovalPending)
	composerText := sanitizeChatViewText(model.Composer)
	composerLines, cursorLine, cursorColumn := wrapChatComposer(composerText, model.CursorLine, model.CursorColumn, max(1, width-4))
	queueReserve := 0
	if len(model.QueuedPrompts) > 0 {
		queueReserve = 1
	}
	maxInputRows := max(1, min(4, height-headerRows-footerRows-controlRows-approvalRows-2-queueReserve))
	inputRows := max(1, min(maxInputRows, len(composerLines)))
	composerRows := inputRows + 2
	spare := max(0, height-headerRows-footerRows-controlRows-approvalRows-composerRows)
	queueRows := min(min(chatQueueViewRowLimit, len(model.QueuedPrompts)), spare)
	completionRows := min(min(6, len(model.Suggestions)), max(0, spare-queueRows))
	activityRows := 0
	if model.Activity != "" && spare > queueRows+completionRows {
		activityRows = 1
	}
	separatorRows := 0
	if spare-queueRows-completionRows-activityRows > 1 && !short {
		separatorRows = 1
	}
	transcriptRows := max(0, spare-queueRows-completionRows-activityRows-separatorRows)
	transcript := model.TranscriptLines
	if transcript == nil {
		transcript = expandChatTranscript(model.Messages, width-4)
	}
	if len(transcript) == 0 {
		transcript = chatWelcomeTranscript(transcriptRows, width-2, model.Mark)
	} else if short {
		transcript = collapseChatBlankLines(transcript)
	}
	visibleTranscript, clippedAbove, clippedBelow := chatTranscriptWindow(transcript, transcriptRows, model.ScrollOffset)

	row := 0
	if short {
		header := chatShortHeader(model)
		frame.Lines[row] = paintChatViewLine(header, "subheader", "", false, width, palette, color)
		frame.addTarget(row, header, chatChangesLabel(model.ChangedFiles), "diff")
	} else {
		frame.Lines[row] = pulseChatHeader(paintChatViewLine(chatViewHeader(model, width, false), "header", "", false, width, palette, color), model, palette, color)
		row++
		subheader := chatViewHeader(model, width, true)
		frame.Lines[row] = paintChatViewLine(subheader, "subheader", "", false, width, palette, color)
		frame.addTarget(row, subheader, chatChangesLabel(model.ChangedFiles), "diff")
	}
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
		frame.Targets = append(frame.Targets, chatTapTarget{row: row, from: 0, to: width, action: "suggestion:" + strconv.Itoa(choiceStart+i)})
		row++
	}
	if approvalRows > 0 {
		line := " ⚠ Approve?  [ y Allow ]  [ n Deny ]"
		frame.Lines[row] = paintChatViewLine(fitChatView(line, width), "approval", "", false, width, palette, color)
		frame.addTarget(row, line, "[ y Allow ]", "approve")
		frame.addTarget(row, line, "[ n Deny ]", "deny")
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
	if short {
		// The controls ride in the bottom border: tap the model or reasoning.
		parts := []string{strings.TrimSpace(sendLabel), controlModel, controlReasoning}
		if meter := textutil.SanitizeText(model.ControlContext); meter != "" {
			parts = append(parts, meter)
		}
		sendLabel = " " + strings.Join(parts, " · ") + " "
	}
	bottom := chatComposerBorder(width, "╰", "╯", sendLabel, visibleStart+inputRows < len(composerLines))
	frame.Lines[row] = paintChatViewLine(bottom, "composer-border", "", false, width, palette, color)
	if short {
		frame.addTarget(row, bottom, controlModel, "model")
		frame.addTarget(row, bottom, controlReasoning, "reasoning")
	}
	row++
	for _, control := range controlLines {
		line := " " + fitChatView(control, width-2)
		frame.Lines[row] = paintChatViewLine(line, "controls", "", false, width, palette, color)
		frame.addTarget(row, line, "◈ Model "+controlModel, "model")
		frame.addTarget(row, line, "◉ Reasoning "+controlReasoning, "reasoning")
		row++
	}
	if footerRows > 0 {
		frame.Lines[height-1] = paintChatViewLine(chatFooter(model, width, clippedAbove, clippedBelow), "footer", "", false, width, palette, color)
	}

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
	if changes := chatChangesLabel(model.ChangedFiles); changes != "" {
		return modelName + "   ·   " + changes + "   ·   " + state
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
	if width < chatNarrowColumns {
		hints = chatPhoneHints(hints)
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
