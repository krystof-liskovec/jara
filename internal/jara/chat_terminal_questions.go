package jara

import (
	"strconv"
	"strings"
)

// chatQuestionMenu tracks the arrow-key choice for the question the agent is
// waiting on. Options come first, then "Type your own answer" when the
// question allows it, then Submit for a multi-select question. Tab starts a
// note: what is typed then goes with the chosen option instead of replacing
// it, as in Claude Code's own picker.
type chatQuestionMenu struct {
	key      string // request and question the cursor and checks belong to
	cursor   int
	checked  map[int]bool
	required bool // Submit was pressed with nothing checked
	noting   bool // the input box holds a note for the answer
}

// chatQuestionNotePrefix marks a note among a question's answers, as Codex's
// own picker sends it; Jara's Claude Code bridge turns it into the
// AskUserQuestion note.
const chatQuestionNotePrefix = "user_note: "

// activeChatQuestion returns the first unanswered question of the oldest
// input request, and its number from 1.
func activeChatQuestion(u *chatUI) (chatQuestion, int, bool) {
	if len(u.questionRequests) == 0 {
		return chatQuestion{}, 0, false
	}
	request := u.questionRequests[0]
	for i, question := range request.params.Questions {
		if _, answered := request.answers[question.ID]; !answered {
			return question, i + 1, true
		}
	}
	return chatQuestion{}, 0, false
}

// chatQuestionRows counts the menu's rows: options, own answer, Submit.
func chatQuestionRows(question chatQuestion) (options, other, submit int) {
	options = len(question.Options)
	other = -1
	submit = -1
	rows := options
	if question.IsOther || options == 0 {
		other, rows = rows, rows+1
	}
	if question.MultiSelect && options > 0 {
		submit = rows
	}
	return options, other, submit
}

// questionMenu returns the menu state for the current question, resetting it
// when another question becomes current.
func (t *chatTerminal) questionMenu() (*chatQuestionMenu, chatQuestion, int, bool) {
	question, number, ok := activeChatQuestion(t.u)
	if !ok || t.picker.active() || t.sessionPicker || t.queueEditor.active() || t.u.viewedAgent() != nil {
		return nil, question, number, false
	}
	key := string(t.u.questionRequests[0].message.ID) + "\x00" + question.ID
	if t.question.key != key {
		t.question = chatQuestionMenu{key: key, checked: map[int]bool{}}
	}
	return &t.question, question, number, true
}

// questionChoices lists the menu above the input box while a question waits.
// Typing a command shows the usual completions instead.
func (t *chatTerminal) questionChoices() ([]chatCompletionChoice, bool) {
	menu, question, _, ok := t.questionMenu()
	if !ok || chatTerminalCommandText(t.editor.Text()) {
		return nil, false
	}
	options, other, submit := chatQuestionRows(question)
	total := options + btoi(other >= 0) + btoi(submit >= 0)
	heading := strings.TrimSpace(question.Header)
	if heading == "" {
		heading = "Question"
	}
	hint := "↑↓ choose · Enter select · Tab add a note"
	if question.MultiSelect {
		hint = "↑↓ choose · Enter or Space check · Tab add a note"
	}
	switch {
	case menu.required:
		hint = "Check at least one option first"
	case menu.noting && question.MultiSelect:
		hint = "Type a note · it goes with Submit · Tab or Esc stops"
	case menu.noting:
		hint = "Type a note · Enter sends it with the option · Tab or Esc stops"
	}
	choices := []chatCompletionChoice{{Value: heading, Description: hint}}
	for i, option := range question.Options {
		value := strconv.Itoa(i+1) + ". " + option.Label
		if question.MultiSelect {
			box := "[ ] "
			if menu.checked[i] {
				box = "[x] "
			}
			value = box + option.Label
		}
		choices = append(choices, chatCompletionChoice{Value: value, Description: option.Description, Selected: menu.cursor == i, Index: i, Total: total})
	}
	if other >= 0 {
		description := "type it in the input box, then press Enter"
		if strings.TrimSpace(t.editor.Text()) != "" && !menu.noting {
			description = "Enter sends what you typed"
		}
		choices = append(choices, chatCompletionChoice{Value: "✎ Type your own answer", Description: description, Selected: menu.cursor == other, Index: other, Total: total})
	}
	if submit >= 0 {
		choices = append(choices, chatCompletionChoice{Value: "Submit", Description: strconv.Itoa(len(menu.checked)) + " checked", Selected: menu.cursor == submit, Index: submit, Total: total})
	}
	return choices, true
}

func chatTerminalCommandText(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!")
}

// handleQuestionKey drives the question menu: arrows move, Enter chooses or
// checks, Space checks, and Enter with typed text answers with that text.
func (t *chatTerminal) handleQuestionKey(input chatEditorInput) (bool, error) {
	menu, question, number, ok := t.questionMenu()
	text := t.editor.Text()
	if !ok || chatTerminalCommandText(text) {
		return false, nil
	}
	options, other, submit := chatQuestionRows(question)
	last := max(options-1, other, submit)
	typed := strings.TrimSpace(text)
	note := ""
	if menu.noting && options > 0 {
		note, typed = typed, ""
	}
	switch {
	case input.Key == chatEditorTab && options > 0:
		menu.noting = !menu.noting
	case input.Key == chatEditorEscape && menu.noting:
		menu.noting = false
	case input.Key == chatEditorUp && !strings.Contains(text, "\n"):
		menu.cursor = max(0, menu.cursor-1)
	case input.Key == chatEditorDown && !strings.Contains(text, "\n"):
		menu.cursor = min(last, menu.cursor+1)
	case input.Key == chatEditorText && input.Text == " " && text == "" && question.MultiSelect && menu.cursor < options:
		menu.toggle(menu.cursor)
	case input.Key != chatEditorEnter:
		return false, nil
	case typed != "" && other >= 0:
		return true, t.answerQuestion(number, append(menu.checkedLabels(question), typed), "")
	case typed != "":
		return false, nil // no own answers here; Enter sends the text as usual
	case menu.cursor < options && question.MultiSelect:
		menu.toggle(menu.cursor)
	case menu.cursor < options:
		return true, t.answerQuestion(number, []string{question.Options[menu.cursor].Label}, note)
	case menu.cursor == submit:
		if len(menu.checked) == 0 {
			menu.required = true
			break
		}
		return true, t.answerQuestion(number, menu.checkedLabels(question), note)
	case menu.cursor == other && note != "":
		return true, t.answerQuestion(number, append(menu.checkedLabels(question), note), "")
	}
	return true, nil // Enter on "Type your own answer" waits for the text
}

func (m *chatQuestionMenu) toggle(index int) {
	if m.checked[index] {
		delete(m.checked, index)
	} else {
		m.checked[index] = true
	}
	m.required = false
}

func (m *chatQuestionMenu) checkedLabels(question chatQuestion) []string {
	var labels []string
	for i, option := range question.Options {
		if m.checked[i] {
			labels = append(labels, option.Label)
		}
	}
	return labels
}

// answerQuestion sends the menu's answer, with the note if one was written,
// and clears the input box.
func (t *chatTerminal) answerQuestion(number int, values []string, note string) error {
	t.editor.SetText("")
	t.u.noteChatComposer("", 0)
	shown := strings.Join(values, ", ")
	if note != "" {
		shown += " — note: " + note
		values = append(values, chatQuestionNotePrefix+note)
	}
	t.u.print(chatWrappedLines("› ", shown, chatQuestionLineWidth))
	return t.u.recordQuestionAnswer(number, values)
}
