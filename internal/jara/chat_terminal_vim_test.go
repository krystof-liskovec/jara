//go:build linux || darwin

package jara

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestChatTerminalVimToggleAndEdit(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	typeChatKeys(t, term, "/vim\r")
	if term.editor.vim != chatVimInsert {
		t.Fatalf("/vim selected %q, want Insert", term.editor.vim)
	}
	typeChatKeys(t, term, "first\n👩‍💻x\x1b0xkddA!")
	if got := term.editor.Text(); got != "x!" || term.editor.vim != chatVimInsert {
		t.Fatalf("draft = %q, mode = %q after Vim editing", got, term.editor.vim)
	}
	typeChatKeys(t, term, "\x1b0\x1b[18~Z")
	if got := term.editor.Text(); got != "Zx!" || term.editor.vim != chatVimOff {
		t.Fatalf("draft = %q, mode = %q after F7", got, term.editor.vim)
	}
	if wire.Len() != 0 {
		t.Fatalf("composer actions unexpectedly reached the agent: %s", wire)
	}
}

func TestChatTerminalVimEscapeKeepsTurnRunning(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	term.u.busy, term.u.turn = true, "active-turn"
	typeChatKeys(t, term, "/vim on\r\x1b")
	if err := term.tick(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	typeChatKeys(t, term, "\x1b\x1b")
	if err := term.tick(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if term.editor.vim != chatVimNormal || wire.Len() != 0 || term.u.cancel {
		t.Fatalf("Escape selected %q and wrote %s", term.editor.vim, wire)
	}
	for _, size := range [][2]int{{40, 12}, {15, 7}} {
		frame := term.frame(size[0], size[1], time.Now())
		view := strings.Join(frame.Lines, "\n")
		if !strings.Contains(view, "NORMAL") && !strings.Contains(view, "[N]") {
			t.Fatalf("Normal mode missing from %dx%d terminal", size[0], size[1])
		}
	}
	typeChatKeys(t, term, "\x03")
	if !strings.Contains(wire.String(), "turn/interrupt") {
		t.Fatalf("Ctrl+C did not interrupt the turn: %s", wire)
	}
}

func TestChatTerminalVimNormalLettersDoNotAnswerApproval(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	typeChatKeys(t, term, "/vim on\r\x1b")
	term.u.approvals = []appMessage{{ID: json.RawMessage("1")}}
	typeChatKeys(t, term, "yn")
	if wire.Len() != 0 || len(term.u.approvals) != 1 || term.editor.Text() != "" {
		t.Fatalf("Normal mode letters changed the approval or draft: %s", wire)
	}
	typeChatKeys(t, term, "in")
	if len(term.u.approvals) != 0 || !strings.Contains(wire.String(), "decline") {
		t.Fatalf("Insert mode did not handle the approval shortcut: %s", wire)
	}
}
