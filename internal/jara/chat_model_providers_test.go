//go:build linux || darwin

package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func testModelBrowser(models ...protocol.CodexModel) *chatModelBrowser {
	browser := newChatModelBrowser()
	browser.models, browser.complete = models, true
	return browser
}

// pickerRows shows the menu as "heading|choice|>selected choice".
func pickerRows(term *chatTerminal) string {
	var rows []string
	for _, choice := range term.suggestions() {
		mark := ""
		if choice.Selected {
			mark = ">"
		}
		rows = append(rows, mark+choice.Value)
	}
	return strings.Join(rows, "|")
}

func TestModelPickerOpensOnProvidersThenTheirModels(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	u := term.u
	u.models = testModelBrowser(
		protocol.CodexModel{ID: "gpt-5.5", Model: "gpt-5.5", DisplayName: "GPT-5.5", IsDefault: true},
		protocol.CodexModel{ID: "gpt-6", Model: "gpt-6", DisplayName: "GPT-6"},
		protocol.CodexModel{ID: "claude/opus", Model: "claude/opus", DisplayName: "Claude Opus"},
		protocol.CodexModel{ID: "claude/sonnet", Model: "claude/sonnet", DisplayName: "Claude Sonnet"},
	)
	u.setEffectiveModel("claude/sonnet")

	// F2 lists the providers, on the current model's.
	typeChatKeys(t, term, "\x1bOQ")
	if got := pickerRows(term); got != "Choose a provider|Codex|>Claude Code" {
		t.Fatalf("providers %s", got)
	}
	// A tap opens a provider, on the current model.
	frame := term.frame(100, 30, time.Now())
	term.targets = frame
	if _, err := term.tap(4, chatRowContaining(frame, "2 models · current")); err != nil {
		t.Fatal(err)
	}
	if got := pickerRows(term); got != "Claude Code models|Claude Opus|>Claude Sonnet" {
		t.Fatalf("Claude Code models %s", got)
	}
	// Escape goes back to the providers, then closes.
	term.handlePickerKey(chatEditorInput{Key: chatEditorEscape})
	if got := pickerRows(term); got != "Choose a provider|Codex|>Claude Code" {
		t.Fatalf("back %s", got)
	}
	typeChatKeys(t, term, "\x1b[A\r") // Codex
	if got := pickerRows(term); got != "Codex models|>GPT-5.5|GPT-6" {
		t.Fatalf("Codex models %s", got)
	}
	typeChatKeys(t, term, "\x1b[B\r")
	if term.picker.active() || u.nextModel != "gpt-6" {
		t.Fatalf("chose %q, picker %q", u.nextModel, term.picker.kind)
	}
	term.handlePickerKey(chatEditorInput{Key: chatEditorEscape})

	// /model opens the providers too, now on the next turn's model.
	if err := term.picker.request(u, "model"); err != nil {
		t.Fatal(err)
	}
	if got := pickerRows(term); got != "Choose a provider|>Codex|Claude Code" {
		t.Fatalf("reopened %s", got)
	}
	term.handlePickerKey(chatEditorInput{Key: chatEditorEscape})
	if term.picker.active() {
		t.Fatal("Escape on the providers closes the picker")
	}
}

func TestModelPickerSkipsProvidersWhenThereIsOne(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	term.u.models = testModelBrowser(
		protocol.CodexModel{ID: "gpt-5.5", Model: "gpt-5.5", DisplayName: "GPT-5.5", IsDefault: true},
		protocol.CodexModel{ID: "gpt-6", Model: "gpt-6", DisplayName: "GPT-6"},
	)
	typeChatKeys(t, term, "\x1bOQ")
	if got := pickerRows(term); got != "Choose a model|>GPT-5.5|GPT-6" {
		t.Fatalf("one provider %s", got)
	}
	typeChatKeys(t, term, "\x1b[B\r")
	if term.u.nextModel != "gpt-6" {
		t.Fatalf("chose %q", term.u.nextModel)
	}
}

func TestArrowsOpenSessionsAndWorkMenus(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	u := term.u
	u.cwd = t.TempDir()
	u.models = testModelBrowser(
		protocol.CodexModel{ID: "gpt-5.5", Model: "gpt-5.5", DisplayName: "GPT-5.5", IsDefault: true},
		protocol.CodexModel{ID: "claude/opus", Model: "claude/opus", DisplayName: "Claude Opus"},
	)
	// While typing, Left moves the cursor.
	typeChatKeys(t, term, "ab\x1b[D")
	if term.sessionPicker || term.editor.CursorByte() != 1 {
		t.Fatalf("Left in text: sessions %v, cursor %d", term.sessionPicker, term.editor.CursorByte())
	}
	term.editor.SetText("")
	// On an empty input box it opens the recent sessions; Left closes them.
	typeChatKeys(t, term, "\x1b[D")
	if !term.sessionPicker {
		t.Fatal("Left on an empty input box opens the sessions")
	}
	typeChatKeys(t, term, "\x1b[D")
	if term.sessionPicker {
		t.Fatal("Left closes the sessions again")
	}
	// In the model picker Right chooses like Enter and Left goes back.
	typeChatKeys(t, term, "\x1bOQ\x1b[C")
	if got := pickerRows(term); got != "Codex models|>GPT-5.5" {
		t.Fatalf("Right opens the provider: %s", got)
	}
	typeChatKeys(t, term, "\x1b[D")
	if got := pickerRows(term); got != "Choose a provider|>Codex|Claude Code" {
		t.Fatalf("Left goes back: %s", got)
	}
	typeChatKeys(t, term, "\x1b[D")
	if term.picker.active() || term.sessionPicker {
		t.Fatalf("Left on the providers closes the picker without opening sessions: %q %v", term.picker.kind, term.sessionPicker)
	}
}

func TestModelListKeepsEveryProvidersModels(t *testing.T) {
	var data []protocol.CodexModel
	for i := range 14 {
		id := fmt.Sprintf("gpt-%d", i)
		data = append(data, protocol.CodexModel{ID: id, Model: id})
	}
	for _, alias := range []string{"default", "opus", "sonnet", "haiku", "fable"} {
		data = append(data, protocol.CodexModel{ID: "claude/" + alias, Model: "claude/" + alias})
	}
	raw, _ := json.Marshal(map[string]any{"data": data})
	browser := newChatModelBrowser()
	if _, err := browser.acceptPage(raw); err != nil {
		t.Fatal(err)
	}
	codex, claude := chatProviderModels(browser.models, protocol.AgentCodex), chatProviderModels(browser.models, protocol.AgentClaude)
	if len(codex) != chatModelMaxShown || len(claude) != 5 || !browser.more {
		t.Fatalf("kept %d Codex and %d Claude Code models, more %v", len(codex), len(claude), browser.more)
	}
}
