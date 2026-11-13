//go:build linux || darwin

package jara

import (
	"strconv"
	"strings"
)

// tap runs the control under a click or tap: the model and reasoning
// labels, menu entries, the approval buttons, and the changed-files label.
func (t *chatTerminal) tap(column, row int) (done bool, err error) {
	action := t.targets.target(row, column)
	if action == "" {
		return false, nil
	}
	t.dirty = true
	switch {
	case action == "model" || action == "reasoning":
		t.openPicker(action)
	case action == "approve" || action == "deny":
		return t.u.line("/" + action)
	case action == "diff":
		t.u.runChatDiffCommand()
		t.scroll(0)
	case action == "diff-close":
		t.u.diff.mode = chatDiffPaneForcedOff
	case action == "diff-ask":
		t.askAboutChanges()
	case strings.HasPrefix(action, "agent:"):
		id := strings.TrimPrefix(action, "agent:")
		t.showAgent(func() { t.u.showChatAgent(id) })
	case strings.HasPrefix(action, "suggestion:"):
		index, _ := strconv.Atoi(strings.TrimPrefix(action, "suggestion:"))
		return t.tapSuggestion(index)
	}
	return false, nil
}

// chatApprovalKey answers a pending approval with a single y or n typed
// into an empty input box.
func chatApprovalKey(u *chatUI, editor *chatEditor, input chatEditorInput) string {
	if len(u.approvals) == 0 || editor.Text() != "" || input.Key != chatEditorText {
		return ""
	}
	switch strings.ToLower(input.Text) {
	case "y":
		return "/approve"
	case "n":
		return "/deny"
	}
	return ""
}

// tapSuggestion chooses a menu entry as Enter would after selecting it.
func (t *chatTerminal) tapSuggestion(index int) (done bool, err error) {
	choices := t.suggestions()
	if index < 0 || index >= len(choices) || choices[index].Total == 0 {
		return false, nil // a heading, not a choice
	}
	choice := choices[index]
	switch {
	case t.sessionPicker:
		t.sessionSelection = choice.Index
		t.handleSessionPickerKey(chatEditorInput{Key: chatEditorEnter})
	case t.picker.active():
		t.picker.selected = choice.Index
		t.handlePickerKey(chatEditorInput{Key: chatEditorEnter})
	default:
		if t.editor.ApplyCompletion(choice.Index) {
			t.u.noteChatComposer(t.editor.Text(), t.editor.CursorByte())
		}
	}
	return false, nil
}

// askAboutChanges starts a question about the changes pane's files in the
// input box, keeping anything already typed.
func (t *chatTerminal) askAboutChanges() {
	question := chatDiffQuestion(&t.u.diff)
	if question == "" {
		return
	}
	text := t.editor.Text()
	if !strings.HasPrefix(text, question) {
		text = question + text
	}
	t.editor.SetText(text)
	t.u.noteChatComposer(t.editor.Text(), t.editor.CursorByte())
	t.scroll(0)
}
