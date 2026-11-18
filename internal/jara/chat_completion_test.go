package jara

import (
	"fmt"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/protocol"
)

func TestChatCompletionCatalogMatchesHelpCommands(t *testing.T) {
	got := map[string]bool{}
	for _, choice := range chatCompletionCatalog() {
		if choice.Description == "" {
			t.Errorf("command %s has no description", choice.Value)
		}
		got[choice.Value] = true
	}
	want := map[string]bool{}
	for _, section := range chatHelpSections {
		for _, entry := range section.entries {
			if strings.HasPrefix(entry.command, "/") {
				want[entry.command] = true
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("catalog and help contain different command counts: catalog=%v help=%v", got, want)
	}
	for command := range want {
		if !got[command] {
			t.Errorf("help command %s is missing from completion", command)
		}
	}
	for command := range got {
		if !want[command] {
			t.Errorf("completion suggests unsupported command %s", command)
		}
	}
}

func TestChatCompletionUsesCachedModelChoicesAndScrolls(t *testing.T) {
	e := newChatEditor(1024)
	context := chatCompletionContext{}
	for i := 1; i <= 10; i++ {
		context.Models = append(context.Models, chatCompletionOption{
			Value:       fmt.Sprintf("provider:model-%02d", i),
			Description: fmt.Sprintf("Model %02d", i),
		})
	}
	e.SetCompletionContext(context)
	editorText(e, "/model ")
	choices := e.Completions()
	if len(choices) != chatEditorCompletionWindow || !choices[0].Selected || choices[0].Total != 10 {
		t.Fatalf("initial completion window is incorrect: %+v", choices)
	}
	for i := 0; i < 8; i++ {
		if !e.Apply(chatEditorInput{Key: chatEditorDown}).Changed {
			t.Fatal("completion selection stopped before the final choice")
		}
	}
	choices = e.Completions()
	if len(choices) != chatEditorCompletionWindow || choices[0].Index != 3 || !choices[len(choices)-1].Selected {
		t.Fatalf("completion window did not scroll with selection: %+v", choices)
	}
	result := e.Apply(chatEditorInput{Key: chatEditorTab})
	if result.Submitted || e.Text() != "/model provider:model-09" {
		t.Fatalf("Tab did not insert the selected provider-qualified model: result=%+v text=%q", result, e.Text())
	}
	if len(e.Completions()) != 0 {
		t.Fatal("applied completion menu remained open")
	}
}

func TestChatCompletionFiltersModelAndSessionArgumentsFromFrozenCaches(t *testing.T) {
	context := chatCompletionContext{
		Models: []chatCompletionOption{
			{Value: "gpt-5-codex", Description: "GPT 5 Codex"},
			{Value: "gpt-5-mini", Description: "GPT 5 Mini"},
			{Value: "other", Description: "Other"},
		},
		Sessions: []chatCompletionOption{
			{Value: "1", Description: "Fix mobile composer"},
			{Value: "2", Description: "Refactor sync flow"},
		},
	}
	e := newChatEditor(1024)
	e.SetCompletionContext(context)
	editorText(e, "/model gpt-5-c")
	choices := e.Completions()
	if len(choices) != 1 || choices[0].Value != "/model gpt-5-codex" {
		t.Fatalf("model argument filter returned wrong choices: %+v", choices)
	}
	result := e.Apply(chatEditorInput{Key: chatEditorEnter})
	if result.Submitted || e.Text() != "/model gpt-5-codex" {
		t.Fatalf("Enter executed or altered model choice: %+v text=%q", result, e.Text())
	}

	e.SetText("/resume 2")
	e.SetCompletionContext(context)
	choices = e.Completions()
	if len(choices) != 1 || choices[0].Value != "/resume 2" || choices[0].Description != "Refactor sync flow" {
		t.Fatalf("session completion did not use displayed choice: %+v", choices)
	}
	e.Apply(chatEditorInput{Key: chatEditorEscape})
	if len(e.Completions()) != 0 {
		t.Fatal("Escape did not dismiss completion choices")
	}
}

func TestChatCompletionContextOnlyExposesCompletedServerCaches(t *testing.T) {
	u := &chatUI{
		models:   &chatModelBrowser{models: []protocol.CodexModel{{ID: "gpt-5"}}, complete: false},
		sessions: &chatSessionBrowser{matches: []Conversation{{ID: "thread-1", Name: "One"}}, complete: false},
	}
	context := chatCompletionContextFromUI(u)
	if len(context.Models) != 0 || len(context.Sessions) != 0 {
		t.Fatalf("incomplete lookup was exposed for numeric selection: %+v", context)
	}
	u.models.complete = true
	u.sessions.complete = true
	context = chatCompletionContextFromUI(u)
	if len(context.Models) != 1 || context.Models[0].Value != "gpt-5" || len(context.Sessions) != 1 || context.Sessions[0].Value != "1" {
		t.Fatalf("completed catalog choices missing from context: %+v", context)
	}
}
