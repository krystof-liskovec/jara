//go:build linux || darwin

package jara

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
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
