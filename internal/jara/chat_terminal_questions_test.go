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
	if len(choices) != 4 || choices[0].Value != "DB" || choices[1].Value != "1. Postgres" || !choices[1].Selected || choices[3].Value != "✎ Type your own answer" {
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
	if hint := term.suggestions()[0].Description; !strings.Contains(hint, "then n adds a note") {
		t.Fatalf("hint %q", hint)
	}
	// After choosing with ↓, n starts a note; Enter sends the option with
	// it, not the text alone.
	typeChatKeys(t, term, "\x1b[B")
	if hint := term.suggestions()[0].Description; !strings.Contains(hint, "· n add a note") {
		t.Fatalf("hint after choosing %q", hint)
	}
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
	// Typed straight away, an answer starting with n stays the user's own.
	typeChatKeys(t, term, "no\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"db":{"answers":["no"]}`) {
		t.Fatalf("own answer %q", wire.String())
	}
	wire.Reset()
	askChatTerminalQuestion(t, term, `[{"id":"db","question":"Which database?","isOther":true,"options":[{"label":"Postgres"}]}]`)
	// Esc stops a note: the text is the user's own answer again.
	typeChatKeys(t, term, "\x1b[A\x1b[AnDuckDB")
	if _, err := term.handleKey(chatTerminalKeyEvent{input: &chatEditorInput{Key: chatEditorEscape}}); err != nil {
		t.Fatal(err)
	}
	typeChatKeys(t, term, "\r")
	if reply := chatQuestionReply(t, wire); !strings.Contains(reply, `"db":{"answers":["DuckDB"]}`) {
		t.Fatalf("reply %q", wire.String())
	}
}
