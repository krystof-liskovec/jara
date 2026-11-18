//go:build linux || darwin

package jara

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func newTestChatTerminal(t *testing.T) (*chatTerminal, *bytes.Buffer) {
	t.Helper()
	t.Setenv("TMUX", "")
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { output.Close() })
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.connected = true
	return newChatTerminal(u, output), &wire
}

func typeChatKeys(t *testing.T, term *chatTerminal, data string) bool {
	t.Helper()
	for _, event := range term.parser.feed([]byte(data)) {
		done, err := term.handleKey(event)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return true
		}
	}
	return false
}

func TestChatTerminalShiftArrowsStepReasoning(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	term.u.effectiveModel = "test-model"
	typeChatKeys(t, term, "draft\x1b[1;2A")
	requests := decodeModelRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "model/list" {
		t.Fatalf("first Shift+Up should load the model catalog: %+v", requests)
	}
	models := []protocol.CodexModel{{
		ID: "test-model", DefaultReasoningEffort: "medium",
		SupportedReasoningEfforts: []protocol.ReasoningEffortOption{{Value: "low"}, {Value: "medium"}, {Value: "xhigh"}},
	}}
	if err := term.handleMessage(appMessage{ID: requests[0].ID, Result: modelPageJSON(t, models, "")}); err != nil {
		t.Fatal(err)
	}
	if got := term.u.nextReasoning; got != "xhigh" {
		t.Fatalf("Shift+Up from the default selected %q, want xhigh", got)
	}
	generation := term.u.reasoningSelectionGeneration
	typeChatKeys(t, term, "\x1b[1;2A")
	if term.u.reasoningSelectionGeneration != generation {
		t.Fatal("Shift+Up at the highest effort should leave the selection alone")
	}
	typeChatKeys(t, term, "\x1b[1;2:1B\x1b[57353;2u")
	if got := term.u.nextReasoning; got != "low" {
		t.Fatalf("two Shift+Down presses selected %q, want low", got)
	}
	if got := term.editor.Text(); got != "draft" {
		t.Fatalf("shortcut changed the draft to %q", got)
	}
	typeChatKeys(t, term, "\r")
	requests = decodeModelRequests(t, wire)
	if len(requests) != 2 || requests[1].Method != "turn/start" || string(requests[1].Params["effort"]) != `"low"` {
		t.Fatalf("expected the draft to start a turn with low effort: %+v", requests)
	}
}

func TestChatTerminalRewindPicker(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	term.u.promptLog = []chatPromptEntry{{turnID: "a", text: "first"}, {turnID: "b", text: "second"}}
	typeChatKeys(t, term, "/rewind\r")
	if term.picker.kind != "rewind" || term.picker.selected != 1 {
		t.Fatalf("picker %q selected %d", term.picker.kind, term.picker.selected)
	}
	if choices := term.suggestions(); len(choices) != 3 || !strings.Contains(choices[1].Value, "first") {
		t.Fatalf("choices %+v", choices)
	}
	typeChatKeys(t, term, "\x1b[A\r")
	requests := rawChatRequests(wire)
	last := requests[len(requests)-1]
	if term.picker.active() || last.Method != "jara/rewind" || !strings.Contains(string(last.Params), `"drop":2`) || !strings.Contains(string(last.Params), `"turnId":"a"`) {
		t.Fatalf("rewind request %+v", requests)
	}
}

func TestChatTerminalKeepsUnsentTextAndQueuesWithTab(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	term.u.threadReady = false
	typeChatKeys(t, term, "hello\r")
	if term.editor.Text() != "hello" || len(rawChatRequests(wire)) != 0 {
		t.Fatalf("text typed before a conversation opens stays in the box: %q", term.editor.Text())
	}
	term.u.threadReady, term.u.busy, term.u.turn = true, true, "turn-1"
	typeChatKeys(t, term, "\t")
	if term.editor.Text() != "" || len(term.u.queuedPrompts) != 1 || term.u.queuedPrompts[0].text != "hello" {
		t.Fatalf("Tab queues the prompt during a turn: %q %+v", term.editor.Text(), term.u.queuedPrompts)
	}
	if done := typeChatKeys(t, term, "\x03"); done {
		t.Fatal("Ctrl-C during a turn cancels it instead of leaving")
	}
	if requests := rawChatRequests(wire); len(requests) != 1 || requests[0].Method != "turn/interrupt" {
		t.Fatalf("interrupt %+v", requests)
	}
}

func TestChatScreenRewritesOnlyChangedLines(t *testing.T) {
	var screen chatScreenState
	first := screen.update(chatFrame{Lines: []string{"a", "b"}, CursorRow: 1, CursorCol: 0}, 10, 2)
	if !strings.Contains(first, "\x1b[2J") || !strings.Contains(first, "\x1b[?25h") {
		t.Fatalf("first frame %q", first)
	}
	second := screen.update(chatFrame{Lines: []string{"a", "c"}, CursorRow: -1}, 10, 2)
	if strings.Contains(second, "\x1b[2J") || strings.Contains(second, "\x1b[1;1H") || !strings.Contains(second, "\x1b[2;1Hc") {
		t.Fatalf("second frame %q", second)
	}
	if third := screen.update(chatFrame{Lines: []string{"a", "c"}, CursorRow: 0}, 10, 2); !strings.Contains(third, "\x1b[?25h") {
		t.Fatalf("the cursor shows again after being hidden: %q", third)
	}
}

type rawChatRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func rawChatRequests(wire *bytes.Buffer) []rawChatRequest {
	decoder := json.NewDecoder(bytes.NewReader(wire.Bytes()))
	var requests []rawChatRequest
	for {
		var request rawChatRequest
		if decoder.Decode(&request) != nil {
			return requests
		}
		requests = append(requests, request)
	}
}

func TestChatFooterLeavesOutAlwaysAvailableKeys(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	footer := func() string {
		frame := term.frame(120, 30, time.Now())
		return frame.Lines[len(frame.Lines)-1]
	}
	removed := []string{"Shift+drag", "PgUp/Dn/wheel", "Shift+Enter", "Enter send", "Ctrl+C", "Tab accept"}
	for _, hint := range removed {
		if strings.Contains(footer(), hint) {
			t.Fatalf("idle footer shows %q: %q", hint, footer())
		}
	}
	for range 40 {
		term.transcript.WriteChatRole("assistant", "line\n")
	}
	if got := footer(); !strings.Contains(got, "↑ older") {
		t.Fatalf("a long transcript still marks older lines: %q", got)
	}
	for _, hint := range removed {
		if strings.Contains(footer(), hint) {
			t.Fatalf("scrolled footer shows %q: %q", hint, footer())
		}
	}
}

func TestMessagesHaveNoBlankRowsAtTheirEdges(t *testing.T) {
	out := &chatTerminalOutput{}
	out.SetChatAgent("Claude Code")
	out.WriteChatRole("user", "fix it\n")
	out.WriteChatRole("assistant", "Looking.")
	out.StartChatTool("c1", "$ ls", "", true, time.Now())
	// Claude Code separates text blocks with a paragraph break, which lands
	// at the start of the reply after a tool call.
	out.WriteChatRole("assistant", "\n\nNow the edits.\n\nSecond paragraph.\n")
	var rows []string
	for _, line := range out.transcriptLines(80) {
		rows = append(rows, strings.TrimRight(line.text, " "))
	}
	got := strings.Join(rows, "\n")
	for _, want := range []string{"fix it\n\n✦ Claude Code", "Looking.\n\n⌁ Tool", "  $ ls\n\n✦ Claude Code", "\n  Now the edits.\n\n  Second paragraph."} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript lacks %q:\n%s", want, got)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("a trailing blank row after the last message:\n%s", got)
	}
}
