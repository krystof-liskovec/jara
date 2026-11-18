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
	"krystof.liskovec/jara/internal/textutil"
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
	if screen := chatScreenText(term.frame(100, 30, time.Now())); strings.Contains(screen, "Queued prompt 1") || strings.Contains(screen, "Queued the prompt") {
		t.Fatal("queuing adds no note to the conversation; the queue rows show it")
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

func TestChatScreenHasNoHintRow(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	term.u.diff.record([]protocol.FileChange{{Path: "a.go", Diff: "+a\n"}}, false)
	for range 40 {
		term.transcript.WriteChatRole("assistant", "line\n")
	}
	frame := term.frame(100, 30, time.Now())
	screen := strings.Join(frame.Lines, "\n")
	for _, gone := range []string{"Message", "Enter send", "Tab ", "F4 ", "older", "Ctrl+C"} {
		if strings.Contains(screen, gone) {
			t.Fatalf("screen shows %q:\n%s", gone, screen)
		}
	}
	// The input box sits right above the controls, which end the screen.
	rows := strings.Split(textutil.TerminalEscapes.ReplaceAllString(screen, ""), "\n")
	last := len(rows) - 1
	if !strings.Contains(rows[last], "◈ ") || !strings.HasPrefix(rows[last-1], "╰─") || !strings.HasPrefix(rows[last-3], "╭─") {
		t.Fatalf("bottom rows:\n%s", strings.Join(rows[last-3:], "\n"))
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

func TestSessionStartedPromptsLookTyped(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	u := term.u
	u.printRole("assistant", "Done with the first task.\n")
	u.print("\nYou> ") // the turn ended; nothing was typed
	sendChatToolEvent(t, u, "jara/prompt/started", map[string]any{"threadId": u.thread, "prompt": "now the second task"})
	sendChatToolEvent(t, u, "jara/prompt/started", map[string]any{"threadId": "other", "prompt": "not this window's"})
	var rows []string
	for _, line := range term.transcript.transcriptLines(80) {
		rows = append(rows, strings.SplitN(line.text, " · ", 2)[0])
	}
	got := strings.Join(rows, "|")
	if got != "✦ Codex|  Done with the first task.||● You|  now the second task" || len(u.promptLog) != 1 {
		t.Fatalf("rows %q, prompts %+v", got, u.promptLog)
	}
}

func TestCtrlDDetachesOnAnEmptyInputBox(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	term.u.persistentSession = true
	// With text typed, Ctrl+D deletes the character at the cursor.
	if done := typeChatKeys(t, term, "abc\x1b[D\x04"); done || term.editor.Text() != "ab" {
		t.Fatalf("delete: done %v, text %q", done, term.editor.Text())
	}
	term.editor.SetText("")
	// On an empty box it detaches: the session keeps running, even idle.
	if done := typeChatKeys(t, term, "\x04"); !done || !strings.Contains(wire.String(), `"jara/detach"`) {
		t.Fatalf("detach: done %v, wire %s", done, wire.String())
	}
	// During a turn the window leaves and the agent keeps working.
	term, wire = newTestChatTerminal(t)
	term.u.persistentSession, term.u.busy, term.u.turn = true, true, "turn-1"
	if done := typeChatKeys(t, term, "\x04"); !done || !strings.Contains(wire.String(), `"jara/detach"`) {
		t.Fatalf("leave during a turn: done %v, wire %s", done, wire.String())
	}
}

func TestIdleCtrlCFinishesTheConversation(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	t.Cleanup(func() { chatPlace = chatPlacement{} })
	open := func() *chatTerminal {
		term, _ := newTestChatTerminal(t)
		term.u.persistentSession, term.u.thread = true, "th"
		chatPlace = chatPlacement{root: "/work/project", session: "s1"}
		return term
	}
	plain := func() Request {
		q := Request{}
		applyChatFinished("/work/project", &q)
		return q
	}
	// Ctrl+C on an idle conversation: the next plain jara starts anew, once.
	if done := typeChatKeys(t, open(), "\x03"); !done {
		t.Fatal("idle Ctrl+C keeps the window")
	}
	if q := plain(); !q.New {
		t.Fatal("the next jara resumed the finished conversation")
	}
	if q := plain(); q.New {
		t.Fatal("the marker outlived its first use")
	}
	// An explicit resume keeps its choice and clears the marker too.
	typeChatKeys(t, open(), "\x03")
	q := Request{ResumeLatest: true}
	applyChatFinished("/work/project", &q)
	if q.New || plain().New {
		t.Fatalf("resume --latest: %+v", q)
	}
	// Ctrl+D leaves without finishing; Ctrl+C during a turn only cancels it.
	typeChatKeys(t, open(), "\x04")
	busy := open()
	busy.u.busy, busy.u.turn = true, "turn-1"
	typeChatKeys(t, busy, "\x03")
	if plain().New {
		t.Fatal("Ctrl+D or a cancelled turn finished the conversation")
	}
}

func TestEscapeStopsARunningTurn(t *testing.T) {
	escape := func(term *chatTerminal) {
		t.Helper()
		term.parser.feed([]byte("\x1b")) // a lone Escape resolves on the next tick
		if err := term.tick(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	interrupts := func(wire *bytes.Buffer) int { return strings.Count(wire.String(), `"turn/interrupt"`) }
	// Idle, Escape does nothing.
	term, wire := newTestChatTerminal(t)
	escape(term)
	if interrupts(wire) != 0 {
		t.Fatal("Escape on an idle conversation sent an interrupt")
	}
	// During a turn it stops it, through either way a key arrives.
	term.u.busy, term.u.turn = true, "turn-1"
	escape(term)
	if interrupts(wire) != 1 {
		t.Fatalf("lone Escape: %s", wire.String())
	}
	if _, err := term.handleKey(chatTerminalKeyEvent{input: &chatEditorInput{Key: chatEditorEscape}}); err != nil || interrupts(wire) != 2 {
		t.Fatalf("Escape key event: %v %s", err, wire.String())
	}
	// With a menu open, Escape closes the menu and the turn goes on.
	term.u.models = testModelBrowser(protocol.CodexModel{ID: "gpt-5.5", Model: "gpt-5.5", DisplayName: "GPT-5.5"})
	term.openPicker("reasoning")
	escape(term)
	if term.picker.active() || interrupts(wire) != 2 {
		t.Fatalf("menu: open %v, %s", term.picker.active(), wire.String())
	}
	// A waiting question is not thrown away by a stray Escape.
	askChatTerminalQuestion(t, term, `[{"id":"q","question":"Which?","options":[{"label":"A"}]}]`)
	escape(term)
	if interrupts(wire) != 2 || len(term.u.questionRequests) != 1 {
		t.Fatalf("question: %s", wire.String())
	}
}
