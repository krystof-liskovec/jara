//go:build linux || darwin

package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func askChatTerminalQuestion(t *testing.T, term *chatTerminal, questions string) {
	t.Helper()
	params := `{"threadId":"thread-test","turnId":"turn","itemId":"ask","questions":` + questions + `}`
	if err := term.u.event(appMessage{ID: json.RawMessage(`"ask-1"`), Method: "item/tool/requestUserInput", Params: json.RawMessage(params)}); err != nil {
		t.Fatal(err)
	}
}

func chatQuestionReply(t *testing.T, wire *bytes.Buffer) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(wire.String()), "\n") {
		if strings.Contains(line, `"id":"ask-1"`) {
			return line
		}
	}
	return ""
}

func TestChatTerminalQuestionArrowsChooseAnOption(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	askChatTerminalQuestion(t, term, `[{"id":"db","header":"DB","question":"Which database?","isOther":true,"options":[{"label":"Postgres","description":"relational"},{"label":"SQLite"}]}]`)
	choices := term.suggestions()
	if len(choices) != 5 || choices[0].Value != "DB" || choices[1].Value != "1. Postgres" || choices[1].Description != "" || !choices[1].Selected ||
		choices[3].Value != "✎ Type your own answer" || choices[4].Value != "↳ relational" || choices[4].Total != 0 {
		t.Fatalf("menu %+v", choices)
	}
	typeChatKeys(t, term, "\x1b[B\x1b[B\x1b[B\x1b[A") // down past the end, then back up
	if !term.suggestions()[2].Selected {
		t.Fatalf("cursor %+v", term.suggestions())
	}
	typeChatKeys(t, term, "\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"db":{"answers":["SQLite"]}`) {
		t.Fatalf("reply %q", wire.String())
	}
	if len(term.u.questionRequests) != 0 || len(term.suggestions()) != 0 {
		t.Fatal("answered question must close the menu")
	}
}

func TestChatTerminalQuestionTakesOwnAnswer(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	askChatTerminalQuestion(t, term, `[{"id":"db","question":"Which database?","isOther":true,"options":[{"label":"Postgres"}]},{"id":"why","question":"Why?"}]`)
	// Slash commands still get their own completions while a question waits.
	typeChatKeys(t, term, "/sta")
	if choices := term.suggestions(); len(choices) == 0 || strings.Contains(choices[0].Value, "Question") {
		t.Fatalf("command completions %+v", choices)
	}
	term.editor.SetText("")
	typeChatKeys(t, term, "DuckDB\r")
	if len(term.u.questionRequests) != 1 || term.editor.Text() != "" {
		t.Fatal("the first answer is saved and the second question is next")
	}
	if choices := term.suggestions(); len(choices) != 2 || choices[1].Value != "✎ Type your own answer" {
		t.Fatalf("free-text question menu %+v", choices)
	}
	typeChatKeys(t, term, "\r") // nothing typed yet: Enter waits
	typeChatKeys(t, term, "it is small\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"db":{"answers":["DuckDB"]}`) || !strings.Contains(reply, `"why":{"answers":["it is small"]}`) {
		t.Fatalf("reply %q", wire.String())
	}
}

func TestChatTerminalQuestionMultiSelect(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	askChatTerminalQuestion(t, term, `[{"id":"x","header":"Extras","question":"Which extras?","isOther":true,"multiSelect":true,"options":[{"label":"Cache"},{"label":"Search"},{"label":"Auth"}]}]`)
	submit := func() { typeChatKeys(t, term, "\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\r") }
	submit() // nothing checked yet
	if chatQuestionReply(t, wire) != "" || !strings.Contains(term.suggestions()[0].Description, "Check at least one") {
		t.Fatalf("empty submit %+v", term.suggestions())
	}
	term.question.cursor = 0
	typeChatKeys(t, term, " \x1b[B\x1b[B\r") // Space checks Cache, Enter checks Auth
	choices := term.suggestions()
	if choices[1].Value != "[x] Cache" || choices[2].Value != "[ ] Search" || choices[3].Value != "[x] Auth" || choices[5].Value != "Submit" {
		t.Fatalf("checks %+v", choices)
	}
	typeChatKeys(t, term, " ") // Space on Auth unchecks it
	typeChatKeys(t, term, "plus logs\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"x":{"answers":["Cache","plus logs"]}`) {
		t.Fatalf("reply %q", wire.String())
	}
}

func TestChatQuestionAnswerAcceptsSeveralNumbers(t *testing.T) {
	question := chatQuestion{MultiSelect: true, Options: []chatQuestionOption{{Label: "a"}, {Label: "b"}, {Label: "c"}}}
	if values, err := resolveChatQuestionAnswer(question, "1, 3"); err != nil || strings.Join(values, "|") != "a|c" {
		t.Fatalf("values %v %v", values, err)
	}
	if _, err := resolveChatQuestionAnswer(question, "1,9"); err == nil {
		t.Fatal("out-of-range numbers without isOther must fail")
	}
}

func TestChatTerminalQuestionNoteGoesWithTheOption(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	askChatTerminalQuestion(t, term, `[{"id":"db","question":"Which database?","isOther":true,"options":[{"label":"Postgres"},{"label":"SQLite"}]},`+
		`{"id":"x","question":"Which extras?","multiSelect":true,"options":[{"label":"Cache"},{"label":"Search"}]}]`)
	if hint := term.suggestions()[0].Description; !strings.Contains(hint, "· n add a note") {
		t.Fatalf("hint %q", hint)
	}
	// On "Type your own answer" n is just a letter, so the hint drops it.
	typeChatKeys(t, term, "\x1b[B\x1b[B")
	if hint := term.suggestions()[0].Description; strings.Contains(hint, "note") {
		t.Fatalf("hint on own answer %q", hint)
	}
	// On an option n starts a note; Enter sends the option with it, not the
	// text alone.
	typeChatKeys(t, term, "\x1b[A")
	typeChatKeys(t, term, "nmust run on a phone\r")
	request := term.u.questionRequests[0]
	if got := request.answers["db"]; len(got) != 2 || got[0] != "SQLite" || got[1] != "user_note: must run on a phone" {
		t.Fatalf("answer with note %q", got)
	}
	// Multi-select without own answers: check, write a note, Submit.
	typeChatKeys(t, term, " nonly if cheap")
	if hint := term.suggestions()[0].Description; !strings.Contains(hint, "goes with Submit") {
		t.Fatalf("note hint %q", hint)
	}
	typeChatKeys(t, term, "\x1b[B\x1b[B\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"x":{"answers":["Cache","user_note: only if cheap"]}`) {
		t.Fatalf("reply %q", wire.String())
	}
}

func TestChatTerminalQuestionNoteCanBeAbandoned(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	askChatTerminalQuestion(t, term, `[{"id":"db","question":"Which database?","isOther":true,"options":[{"label":"Postgres"}]}]`)
	// On the first option, highlighted from the start, n starts a note.
	typeChatKeys(t, term, "nquick\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"db":{"answers":["Postgres","user_note: quick"]}`) {
		t.Fatalf("note on the default option %q", wire.String())
	}
	// An own answer starting with n is typed on "Type your own answer".
	wire.Reset()
	askChatTerminalQuestion(t, term, `[{"id":"db","question":"Which database?","isOther":true,"options":[{"label":"Postgres"}]}]`)
	typeChatKeys(t, term, "\x1b[Bno\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"db":{"answers":["no"]}`) {
		t.Fatalf("own answer %q", wire.String())
	}
	wire.Reset()
	askChatTerminalQuestion(t, term, `[{"id":"store","question":"Which store?","isOther":true,"options":[{"label":"Postgres"}]}]`)
	// Esc stops a note: the text is the user's own answer again.
	typeChatKeys(t, term, "nDuckDB")
	if _, err := term.handleKey(chatTerminalKeyEvent{input: &chatEditorInput{Key: chatEditorEscape}}); err != nil {
		t.Fatal(err)
	}
	typeChatKeys(t, term, "\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"store":{"answers":["DuckDB"]}`) {
		t.Fatalf("reply %q", wire.String())
	}
}

func TestQuestionsReadCleanlyInTheConversation(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	long := "Jara passes the servers as launch flags each time it starts an agent; the config files stay untouched."
	askChatTerminalQuestion(t, term, `[
		{"id":"delivery","header":"Delivery","question":"How should Jara hand over its MCP servers?","options":[{"label":"At launch","description":"`+long+`"},{"label":"Write configs"}]},
		{"id":"scope","header":"Scope","question":"Per host or per workspace?","options":[{"label":"Per host"},{"label":"Per workspace"}]}]`)
	typeChatKeys(t, term, "\r")
	typeChatKeys(t, term, "\r")
	var rows []string
	for _, line := range term.transcript.transcriptLines(100) {
		if text := strings.TrimSpace(line.text); text != "" && !strings.HasPrefix(text, "•") {
			rows = append(rows, text)
		}
	}
	want := []string{
		"Delivery · question 1 of 2", "How should Jara hand over its MCP servers?", "› At launch",
		"Scope · question 2 of 2", "Per host or per workspace?", "› Per host",
		"Answers sent to the agent.",
	}
	if got := strings.Join(rows, "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("conversation:\n%s", got)
	}
	// Without the menu, plain output lists the options; long ones wrap
	// under their text instead of repeating the number.
	if wrapped := chatWrappedLines("  1) ", long, 40); strings.Count(wrapped, "1)") != 1 || !strings.Contains(wrapped, "\n     ") {
		t.Fatalf("wrapped option:\n%s", wrapped)
	}
}

func TestQuestionOptionDescriptionsWrapUnderTheMenu(t *testing.T) {
	long := strings.Repeat("This folder's conversation keeps going on the chosen host. ", 3)
	rows := chatQuestionDetail(long, 60)
	if len(rows) < 3 || !strings.HasPrefix(rows[0].Value, "↳ This folder's") || !strings.HasPrefix(rows[1].Value, "  ") {
		t.Fatalf("rows %+v", rows)
	}
	for _, row := range rows {
		if len([]rune(row.Value)) > 60 || strings.Contains(row.Value, "…") && row != rows[len(rows)-1] || row.Total != 0 {
			t.Fatalf("row %q", row.Value)
		}
	}
	if rows := chatQuestionDetail(strings.Repeat("word ", 200), 60); len(rows) != chatQuestionDetailRows || !strings.HasSuffix(rows[len(rows)-1].Value, "…") {
		t.Fatalf("a very long description is not capped: %d rows", len(rows))
	}
}
