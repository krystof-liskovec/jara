package jara

import (
	"strings"

	"github.com/rivo/uniseg"
)

type chatEditorKey string

const (
	chatEditorText            chatEditorKey = "text"
	chatEditorPaste           chatEditorKey = "paste"
	chatEditorLeft            chatEditorKey = "left"
	chatEditorRight           chatEditorKey = "right"
	chatEditorHome            chatEditorKey = "home"
	chatEditorEnd             chatEditorKey = "end"
	chatEditorBackspace       chatEditorKey = "backspace"
	chatEditorDelete          chatEditorKey = "delete"
	chatEditorUp              chatEditorKey = "up"
	chatEditorDown            chatEditorKey = "down"
	chatEditorTab             chatEditorKey = "tab"
	chatEditorEnter           chatEditorKey = "enter"
	chatEditorEscape          chatEditorKey = "escape"
	chatEditorModelPicker     chatEditorKey = "model-picker"
	chatEditorReasoningPicker chatEditorKey = "reasoning-picker"
)

type chatEditorInput struct {
	Key  chatEditorKey
	Text string
}

type chatEditorResult struct {
	Submitted    bool
	Value        string
	Changed      bool
	LimitReached bool
}

type chatCompletionChoice struct {
	Value       string
	Description string
	Selected    bool
	Index       int
	Total       int
}

const (
	chatEditorCompletionWindow = 6
	chatEditorHistoryLimit     = 100
)

type chatEditor struct {
	text                string
	cursor              int
	maxBytes            int
	history             []string
	historyIndex        int
	historyDraft        string
	context             chatCompletionContext
	candidates          []chatEditorCandidate
	selected            int
	dismissed           bool
	completionsDisabled bool
}

func newChatEditor(maxBytes int) *chatEditor {
	if maxBytes <= 0 {
		maxBytes = maxBackgroundPrompt
	}
	return &chatEditor{maxBytes: maxBytes, historyIndex: 0}
}

func (e *chatEditor) Text() string    { return e.text }
func (e *chatEditor) CursorByte() int { return e.cursor }

// SetText restores a rejected submission to the composer without adding it to
// history. Input is sanitized and bounded using the same rules as pasted text.
func (e *chatEditor) SetText(text string) {
	text = cleanChatEditorText(text)
	text, _ = truncateChatGraphemes(text, e.maxBytes)
	e.text = text
	e.cursor = len(text)
	e.afterEdit()
}

// CursorPosition returns zero-based line and terminal-cell column values.
func (e *chatEditor) CursorPosition() (line, column int) {
	prefix := e.text[:e.cursor]
	line = strings.Count(prefix, "\n")
	if split := strings.LastIndexByte(prefix, '\n'); split >= 0 {
		prefix = prefix[split+1:]
	}
	return line, uniseg.StringWidth(prefix)
}

func (e *chatEditor) SetCompletionContext(context chatCompletionContext) {
	e.context = context
	e.refreshCompletions(false)
}

func (e *chatEditor) SetCompletionsDisabled(disabled bool) {
	e.completionsDisabled = disabled
	e.refreshCompletions(true)
}

func (e *chatEditor) Completions() []chatCompletionChoice {
	if e.dismissed || len(e.candidates) == 0 {
		return nil
	}
	start := 0
	if e.selected >= chatEditorCompletionWindow {
		start = e.selected - chatEditorCompletionWindow + 1
	}
	end := start + chatEditorCompletionWindow
	if end > len(e.candidates) {
		end = len(e.candidates)
	}
	choices := make([]chatCompletionChoice, 0, end-start)
	for index := start; index < end; index++ {
		choice := e.candidates[index].choice
		choice.Selected = index == e.selected
		choice.Index = index
		choice.Total = len(e.candidates)
		choices = append(choices, choice)
	}
	return choices
}

func (e *chatEditor) Apply(input chatEditorInput) chatEditorResult {
	result := chatEditorResult{}
	switch input.Key {
	case chatEditorText, chatEditorPaste:
		insert, limitReached := e.insert(input.Text)
		result.Changed, result.LimitReached = insert, limitReached
	case chatEditorLeft:
		result.Changed = e.moveLeft()
	case chatEditorRight:
		result.Changed = e.moveRight()
	case chatEditorHome:
		result.Changed = e.moveHome()
	case chatEditorEnd:
		result.Changed = e.moveEnd()
	case chatEditorBackspace:
		result.Changed = e.backspace()
	case chatEditorDelete:
		result.Changed = e.deleteForward()
	case chatEditorUp:
		result.Changed = e.moveUp()
	case chatEditorDown:
		result.Changed = e.moveDown()
	case chatEditorTab:
		result.Changed = e.applySelectedCompletion()
	case chatEditorEnter:
		if !e.dismissed && len(e.candidates) > 0 {
			result.Changed = e.applySelectedCompletion()
			break
		}
		result.Submitted = true
		result.Value = e.text
		e.remember(result.Value)
		e.text = ""
		e.cursor = 0
		e.historyIndex = len(e.history)
		e.historyDraft = ""
		e.resetCompletions()
		result.Changed = true
	case chatEditorEscape:
		if len(e.candidates) > 0 {
			e.dismissed = true
			result.Changed = true
		}
	}
	return result
}

func (e *chatEditor) insert(raw string) (changed, limitReached bool) {
	if raw == "" {
		return false, false
	}
	insert := cleanChatEditorText(raw)
	available := e.maxBytes - len(e.text)
	insert, truncated := truncateChatGraphemes(insert, available)
	if insert == "" {
		return false, truncated
	}
	e.text = e.text[:e.cursor] + insert + e.text[e.cursor:]
	e.cursor += len(insert)
	e.afterEdit()
	return true, truncated
}

func cleanChatEditorText(text string) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\t", "    ")
	return chatText(text)
}

func truncateChatGraphemes(text string, maxBytes int) (string, bool) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(text) <= maxBytes {
		return text, false
	}
	limit := 0
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		_, end := graphemes.Positions()
		if end > maxBytes {
			break
		}
		limit = end
	}
	return text[:limit], true
}

func (e *chatEditor) moveLeft() bool {
	if e.cursor == 0 {
		return false
	}
	previous := 0
	graphemes := uniseg.NewGraphemes(e.text)
	for graphemes.Next() {
		start, end := graphemes.Positions()
		if end >= e.cursor {
			e.cursor = start
			e.afterCursorMove()
			return true
		}
		previous = start
	}
	e.cursor = previous
	e.afterCursorMove()
	return true
}

func (e *chatEditor) moveRight() bool {
	if e.cursor >= len(e.text) {
		return false
	}
	graphemes := uniseg.NewGraphemes(e.text)
	for graphemes.Next() {
		start, end := graphemes.Positions()
		if start <= e.cursor && end > e.cursor {
			e.cursor = end
			e.afterCursorMove()
			return true
		}
	}
	return false
}

func (e *chatEditor) moveHome() bool {
	lineStart := strings.LastIndexByte(e.text[:e.cursor], '\n') + 1
	if lineStart == e.cursor {
		return false
	}
	e.cursor = lineStart
	e.afterCursorMove()
	return true
}

func (e *chatEditor) moveEnd() bool {
	lineEnd := len(e.text)
	if next := strings.IndexByte(e.text[e.cursor:], '\n'); next >= 0 {
		lineEnd = e.cursor + next
	}
	if lineEnd == e.cursor {
		return false
	}
	e.cursor = lineEnd
	e.afterCursorMove()
	return true
}

func (e *chatEditor) backspace() bool {
	if e.cursor == 0 {
		return false
	}
	previous := e.cursor
	e.moveLeft()
	e.text = e.text[:e.cursor] + e.text[previous:]
	e.afterEdit()
	return true
}

func (e *chatEditor) deleteForward() bool {
	if e.cursor >= len(e.text) {
		return false
	}
	next := e.cursor
	e.moveRight()
	e.text = e.text[:next] + e.text[e.cursor:]
	e.cursor = next
	e.afterEdit()
	return true
}

func (e *chatEditor) moveUp() bool {
	if e.completionVisible() {
		if e.selected > 0 {
			e.selected--
			return true
		}
		return false
	}
	if len(e.history) == 0 || e.historyIndex == 0 {
		return false
	}
	if e.historyIndex == len(e.history) {
		e.historyDraft = e.text
	}
	e.historyIndex--
	e.text = e.history[e.historyIndex]
	e.cursor = len(e.text)
	e.afterCursorMove()
	return true
}

func (e *chatEditor) moveDown() bool {
	if e.completionVisible() {
		if e.selected+1 < len(e.candidates) {
			e.selected++
			return true
		}
		return false
	}
	if e.historyIndex >= len(e.history) {
		return false
	}
	e.historyIndex++
	if e.historyIndex == len(e.history) {
		e.text = e.historyDraft
	} else {
		e.text = e.history[e.historyIndex]
	}
	e.cursor = len(e.text)
	e.afterCursorMove()
	return true
}

func (e *chatEditor) applySelectedCompletion() bool {
	if e.dismissed || len(e.candidates) == 0 || e.selected < 0 || e.selected >= len(e.candidates) {
		return false
	}
	candidate := e.candidates[e.selected]
	if candidate.start < 0 || candidate.end < candidate.start || candidate.end > len(e.text) {
		return false
	}
	replacement := candidate.replacement + candidate.append
	updated := e.text[:candidate.start] + replacement + e.text[candidate.end:]
	if len(updated) > e.maxBytes {
		return false
	}
	e.text = updated
	e.cursor = candidate.start + len(replacement)
	e.dismissed = true
	e.candidates = nil
	e.historyIndex = len(e.history)
	e.historyDraft = ""
	return true
}

func (e *chatEditor) remember(text string) {
	if strings.TrimSpace(text) == "" || len(e.history) > 0 && e.history[len(e.history)-1] == text {
		return
	}
	e.history = append(e.history, text)
	if len(e.history) > chatEditorHistoryLimit {
		e.history = append([]string(nil), e.history[len(e.history)-chatEditorHistoryLimit:]...)
	}
}

func (e *chatEditor) afterEdit() {
	e.historyIndex = len(e.history)
	e.historyDraft = ""
	e.refreshCompletions(true)
}

func (e *chatEditor) afterCursorMove() {
	e.refreshCompletions(false)
}

func (e *chatEditor) resetCompletions() {
	e.candidates = nil
	e.selected = 0
	e.dismissed = false
}

func (e *chatEditor) completionVisible() bool {
	return !e.dismissed && len(e.candidates) > 0
}

func (e *chatEditor) refreshCompletions(resetSelection bool) {
	if e.completionsDisabled {
		e.candidates = nil
		e.selected = 0
		e.dismissed = false
		return
	}
	if resetSelection {
		e.dismissed = false
		e.selected = 0
	}
	e.candidates = chatCompletionCandidates(e.text, e.cursor, e.context)
	if e.selected >= len(e.candidates) {
		e.selected = max(0, len(e.candidates)-1)
	}
}
