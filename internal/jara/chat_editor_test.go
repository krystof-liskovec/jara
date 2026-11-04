package jara

import (
	"strings"
	"testing"
)

func editorText(e *chatEditor, text string) {
	e.Apply(chatEditorInput{Key: chatEditorText, Text: text})
}

func TestChatEditorUsesGraphemeBoundariesAndCellCursor(t *testing.T) {
	e := newChatEditor(256)
	family := "👨‍👩‍👧‍👦"
	accented := "e\u0301"
	editorText(e, "a"+family+accented+"z")
	if e.CursorByte() != len(e.Text()) {
		t.Fatal("cursor did not advance to the end")
	}
	e.Apply(chatEditorInput{Key: chatEditorLeft})
	if e.Text()[:e.CursorByte()] != "a"+family+accented {
		t.Fatal("left arrow split a grapheme")
	}
	e.Apply(chatEditorInput{Key: chatEditorBackspace})
	if e.Text() != "a"+family+"z" {
		t.Fatalf("backspace did not remove one grapheme: %q", e.Text())
	}
	e.Apply(chatEditorInput{Key: chatEditorHome})
	e.Apply(chatEditorInput{Key: chatEditorRight})
	if e.CursorByte() != 1 {
		t.Fatalf("cursor entered a multi-byte grapheme: %d", e.CursorByte())
	}
	e.Apply(chatEditorInput{Key: chatEditorDelete})
	if e.Text() != "az" {
		t.Fatalf("delete did not remove the entire family grapheme: %q", e.Text())
	}

	e.SetText("a界\n🙂x")
	line, column := e.CursorPosition()
	if line != 1 || column != 3 {
		t.Fatalf("cursor position = (%d,%d), want line 1 column 3", line, column)
	}
	e.Apply(chatEditorInput{Key: chatEditorHome})
	if e.CursorByte() != len("a界\n") {
		t.Fatalf("Home did not move to current line start: %d", e.CursorByte())
	}
	e.Apply(chatEditorInput{Key: chatEditorEnd})
	if e.CursorByte() != len(e.Text()) {
		t.Fatal("End did not move to current line end")
	}
}

func TestChatEditorPasteIsBoundedTextAndNeverSubmits(t *testing.T) {
	e := newChatEditor(32)
	paste := "first\r\n/approve\nsecond"
	result := e.Apply(chatEditorInput{Key: chatEditorPaste, Text: paste})
	if result.Submitted || e.Text() != "first\n/approve\nsecond" {
		t.Fatalf("paste was submitted or changed unexpectedly: result=%+v text=%q", result, e.Text())
	}
	if strings.ContainsRune(e.Text(), '\r') {
		t.Fatal("carriage return was not normalized")
	}

	bounded := newChatEditor(5)
	result = bounded.Apply(chatEditorInput{Key: chatEditorPaste, Text: "a🙂b"})
	if !result.LimitReached || result.Submitted || bounded.Text() != "a🙂" {
		t.Fatalf("paste limit split or failed to report: result=%+v text=%q", result, bounded.Text())
	}
	result = bounded.Apply(chatEditorInput{Key: chatEditorEnter})
	if !result.Submitted || result.Value != "a🙂" {
		t.Fatalf("explicit Enter did not submit the composed text: %+v", result)
	}
}

func TestChatEditorHistoryCyclesAndRestoresCurrentDraft(t *testing.T) {
	e := newChatEditor(128)
	editorText(e, "one")
	if result := e.Apply(chatEditorInput{Key: chatEditorEnter}); !result.Submitted || result.Value != "one" {
		t.Fatalf("first submit failed: %+v", result)
	}
	editorText(e, "two")
	e.Apply(chatEditorInput{Key: chatEditorEnter})
	editorText(e, "draft")
	e.Apply(chatEditorInput{Key: chatEditorUp})
	if e.Text() != "two" {
		t.Fatalf("history up = %q, want two", e.Text())
	}
	e.Apply(chatEditorInput{Key: chatEditorUp})
	if e.Text() != "one" {
		t.Fatalf("second history up = %q, want one", e.Text())
	}
	e.Apply(chatEditorInput{Key: chatEditorDown})
	e.Apply(chatEditorInput{Key: chatEditorDown})
	if e.Text() != "draft" {
		t.Fatalf("history down did not restore draft: %q", e.Text())
	}
}

func TestChatEditorCompletionRequiresApplyBeforeSubmit(t *testing.T) {
	e := newChatEditor(256)
	editorText(e, "/mo")
	choices := e.Completions()
	if len(choices) < 2 || choices[0].Value != "/model" || choices[1].Value != "/models" {
		t.Fatalf("command completion did not filter the catalog: %+v", choices)
	}
	result := e.Apply(chatEditorInput{Key: chatEditorEnter})
	if result.Submitted || e.Text() != "/model " {
		t.Fatalf("Enter executed instead of applying the completion: result=%+v text=%q", result, e.Text())
	}
	result = e.Apply(chatEditorInput{Key: chatEditorEnter})
	if !result.Submitted || result.Value != "/model " {
		t.Fatalf("second Enter did not submit the applied command: %+v", result)
	}

	editorText(e, "//approve")
	if len(e.Completions()) != 0 {
		t.Fatal("escaped slash text was treated as a command")
	}
}
