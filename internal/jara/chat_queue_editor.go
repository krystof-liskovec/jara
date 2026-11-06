package jara

import (
	"strconv"
	"strings"
)

type chatQueueEditor struct {
	id       uint64
	original string
}

func (e *chatQueueEditor) active() bool { return e.id != 0 }

func (e *chatQueueEditor) begin(u *chatUI, editor *chatEditor, id uint64) bool {
	_, number, prompt, ok := u.queuedPromptByID(id)
	if !ok {
		return false
	}
	e.id, e.original = id, prompt.text
	editor.SetCompletionsDisabled(true)
	editor.SetCompletionContext(chatCompletionContext{})
	editor.SetText(prompt.text)
	u.print("Editing queued prompt " + strconv.Itoa(number) + ". Enter saves; Esc cancels.\n")
	return true
}

func (e *chatQueueEditor) cancel(u *chatUI, editor *chatEditor) {
	e.id, e.original = 0, ""
	editor.SetCompletionContext(chatCompletionContextFromUI(u))
	editor.SetCompletionsDisabled(false)
	editor.SetText("")
	u.print("Queue edit cancelled.\n")
}

func (e *chatQueueEditor) save(u *chatUI, editor *chatEditor, text string) {
	index, number, _, ok := u.queuedPromptByID(e.id)
	if !ok {
		e.id, e.original = 0, ""
		editor.SetCompletionContext(chatCompletionContextFromUI(u))
		editor.SetCompletionsDisabled(false)
		editor.SetText(strings.TrimSpace(text))
		u.print("That queued prompt was already dispatched. The edited text remains in the composer.\n")
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		u.print("Queued prompts cannot be empty. Edit the prompt or press Esc to cancel.\n")
		editor.SetText(e.original)
		return
	}
	u.queuedPrompts[index].text = text
	e.id, e.original = 0, ""
	editor.SetCompletionContext(chatCompletionContextFromUI(u))
	editor.SetCompletionsDisabled(false)
	editor.SetText("")
	u.print("Updated queued prompt " + strconv.Itoa(number) + ".\n")
}
