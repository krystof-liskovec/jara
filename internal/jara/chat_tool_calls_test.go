//go:build linux || darwin

package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sendChatToolEvent(t *testing.T, u *chatUI, method string, params map[string]any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.event(appMessage{Method: method, Params: raw}); err != nil {
		t.Fatal(err)
	}
}

func chatScreenText(frame chatFrame) string {
	return strings.Join(frame.Lines, "\n")
}

func TestToolCallsShowOneRowEachAndOpenOnClick(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	u := term.u
	exit := 1
	sendChatToolEvent(t, u, "item/started", map[string]any{"item": map[string]any{"id": "c1", "type": "commandExecution", "command": "go test ./..."}})
	sendChatToolEvent(t, u, "item/started", map[string]any{"item": map[string]any{"id": "c2", "type": "commandExecution", "command": "git status"}})
	// Output arrives for both commands at once; each keeps its own.
	sendChatToolEvent(t, u, "item/commandExecution/outputDelta", map[string]any{"itemId": "c1", "delta": "--- FAIL: TestX\n"})
	sendChatToolEvent(t, u, "item/commandExecution/outputDelta", map[string]any{"itemId": "c2", "delta": "nothing to commit\n"})
	sendChatToolEvent(t, u, "item/commandExecution/outputDelta", map[string]any{"itemId": "c1", "delta": "FAIL\n"})
	sendChatToolEvent(t, u, "item/completed", map[string]any{"item": map[string]any{"id": "c1", "type": "commandExecution", "command": "go test ./...", "exitCode": exit}})
	sendChatToolEvent(t, u, "item/completed", map[string]any{"item": map[string]any{"id": "f1", "type": "fileChange", "status": "completed",
		"changes": []any{map[string]any{"path": "a.go", "kind": "update", "diff": "@@ -1 +1 @@\n-old\n+new"}}}})

	frame := term.frame(100, 30, time.Now())
	screen := chatScreenText(frame)
	for _, want := range []string{"▸ $ go test ./... · 2 lines · exit 1", "▸ $ git status · 1 line", "▸ Edited a.go · +1 −1"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("screen lacks %q:\n%s", want, screen)
		}
	}
	if strings.Count(screen, "⌁ Tool") != 1 || strings.Contains(screen, "FAIL: TestX") || strings.Contains(screen, "+new") {
		t.Fatalf("tool calls share one header and hide their output:\n%s", screen)
	}

	row := chatRowContaining(frame, "$ go test")
	term.targets = frame
	if done, err := term.tap(10, row); done || err != nil {
		t.Fatal(done, err)
	}
	frame = term.frame(100, 30, time.Now())
	term.targets = frame
	screen = chatScreenText(frame)
	if !strings.Contains(frame.Lines[row], "▾ $ go test ./...") || !strings.Contains(screen, "--- FAIL: TestX") || strings.Contains(screen, "nothing to commit") || term.scrollOffset != 0 {
		t.Fatalf("the clicked row opens in place with only its output:\n%s", screen)
	}
	if _, err := term.tap(10, row); err != nil {
		t.Fatal(err)
	}
	if screen = chatScreenText(term.frame(100, 30, time.Now())); strings.Contains(screen, "FAIL: TestX") || term.scrollOffset != 0 {
		t.Fatalf("a second click closes it again (scroll %d):\n%s", term.scrollOffset, screen)
	}
}

func TestToolRowsLeaveOtherOutputAlone(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	sendChatToolEvent(t, term.u, "item/started", map[string]any{"item": map[string]any{"id": "c1", "type": "commandExecution", "command": "cat <<'EOF' > notes.txt\nhello\nEOF"}})
	term.u.printRole("tool", "$ ls\nnotes.txt\n") // a command the user ran
	screen := chatScreenText(term.frame(100, 30, time.Now()))
	if !strings.Contains(screen, "▸ $ cat <<'EOF' > notes.txt · 2 lines") || !strings.Contains(screen, "  notes.txt") {
		t.Fatalf("user output stays visible below the agent's row:\n%s", screen)
	}
	markdown := term.transcript.ChatMarkdown()
	if !strings.Contains(markdown, "```text\n$ cat <<'EOF' > notes.txt\nhello\nEOF\n```") {
		t.Fatalf("export keeps the whole command:\n%s", markdown)
	}
}

func TestToolCallsPrintInFullOutsideTheTerminal(t *testing.T) {
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	sendChatToolEvent(t, u, "item/started", map[string]any{"item": map[string]any{"id": "c1", "type": "commandExecution", "command": "ls"}})
	sendChatToolEvent(t, u, "item/commandExecution/outputDelta", map[string]any{"itemId": "c1", "delta": "a.go\n"})
	if out.String() != "\n$ ls\na.go\n" {
		t.Fatalf("plain output %q", out.String())
	}
}

func TestOpeningAToolCallKeepsItsRowInPlace(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	u := term.u
	u.printRole("assistant", strings.Repeat("earlier\n", 40))
	sendChatToolEvent(t, u, "item/started", map[string]any{"item": map[string]any{"id": "c1", "type": "commandExecution", "command": "go test ./..."}})
	sendChatToolEvent(t, u, "item/commandExecution/outputDelta", map[string]any{"itemId": "c1", "delta": strings.Repeat("ok\n", 30)})
	u.printRole("assistant", strings.Repeat("later\n", 8))

	frame := term.frame(100, 30, time.Now())
	term.targets = frame
	row := chatRowContaining(frame, "▸ $ go test")
	if row < 0 {
		t.Fatalf("no tool row:\n%s", chatScreenText(frame))
	}
	if _, err := term.tap(10, row); err != nil {
		t.Fatal(err)
	}
	frame = term.frame(100, 30, time.Now())
	if !strings.Contains(frame.Lines[row], "▾ $ go test") || !strings.Contains(frame.Lines[row+1], "ok") || term.scrollOffset == 0 {
		t.Fatalf("row %d moved after opening (scroll %d):\n%s", row, term.scrollOffset, chatScreenText(frame))
	}
	term.targets = frame
	if _, err := term.tap(10, row); err != nil {
		t.Fatal(err)
	}
	if frame = term.frame(100, 30, time.Now()); !strings.Contains(frame.Lines[row], "▸ $ go test") || term.scrollOffset != 0 {
		t.Fatalf("closing returns to the newest output (scroll %d):\n%s", term.scrollOffset, chatScreenText(frame))
	}
}

func chatRowContaining(frame chatFrame, text string) int {
	for row, line := range frame.Lines {
		if strings.Contains(line, text) {
			return row
		}
	}
	return -1
}
