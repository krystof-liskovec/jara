//go:build linux || darwin

package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/textutil"
)

func plainChatScreen(frame chatFrame) string {
	return textutil.TerminalEscapes.ReplaceAllString(strings.Join(frame.Lines, "\n"), "")
}

func TestLongApprovalCommandsOpenOnClick(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("echo step-%02d", i))
	}
	params, _ := json.Marshal(map[string]any{"itemId": "cmd", "command": strings.Join(lines, "\n"), "reason": "Run the steps"})
	if err := term.u.event(appMessage{ID: json.RawMessage(`"approve-1"`), Method: "item/commandExecution/requestApproval", Params: params}); err != nil {
		t.Fatal(err)
	}
	frame := term.frame(100, 60, time.Now())
	screen := plainChatScreen(frame)
	if !strings.Contains(screen, "echo step-08") || strings.Contains(screen, "echo step-09") || !strings.Contains(screen, "▸ 22 more lines of the command") {
		t.Fatalf("collapsed approval:\n%s", screen)
	}
	if strings.Count(screen, "• Jara") != 1 || !strings.Contains(screen, "Reason: Run the steps") || !strings.Contains(screen, "to allow once") {
		t.Fatalf("the approval is one message with its reason and keys:\n%s", screen)
	}
	term.targets = frame
	if _, err := term.tap(10, chatRowContaining(frame, "more lines of the command")); err != nil {
		t.Fatal(err)
	}
	if screen = plainChatScreen(term.frame(100, 60, time.Now())); !strings.Contains(screen, "echo step-30") || !strings.Contains(screen, "▾ 22 more lines") {
		t.Fatalf("opened approval:\n%s", screen)
	}

	// A short command shows whole, without a row to open.
	params, _ = json.Marshal(map[string]any{"itemId": "cmd2", "command": strings.Join(lines[:10], "\n")})
	if err := term.u.event(appMessage{ID: json.RawMessage(`"approve-2"`), Method: "item/commandExecution/requestApproval", Params: params}); err != nil {
		t.Fatal(err)
	}
	if screen = plainChatScreen(term.frame(100, 80, time.Now())); strings.Count(screen, "more lines of the command") != 1 {
		t.Fatalf("a 10-line command was folded:\n%s", screen)
	}
}
