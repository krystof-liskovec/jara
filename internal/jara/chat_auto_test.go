package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func TestChatAutoCommandSelectsReviewer(t *testing.T) {
	t.Setenv("TMUX", "")
	var output bytes.Buffer
	u := &chatUI{out: &output}
	if u.approvalsReviewer() != protocol.AutoReviewer {
		t.Fatal("auto mode must be on by default")
	}
	u.runChatAutoCommand("/auto off")
	if u.approvalsReviewer() != "user" || !strings.Contains(output.String(), "Auto mode is off") {
		t.Fatalf("auto off: %q", output.String())
	}
	u.busy = true
	u.runChatAutoCommand("/auto")
	if u.approvalsReviewer() != protocol.AutoReviewer || !strings.Contains(output.String(), "applies from the next turn") {
		t.Fatalf("auto on: %q", output.String())
	}
	u.runChatAutoCommand("/auto maybe")
	if !strings.Contains(output.String(), "Use /auto, /auto on") || !isChatAutoCommand(" /auto status") || isChatAutoCommand("/automatic") {
		t.Fatalf("usage: %q", output.String())
	}
}

func TestShiftTabTogglesAutoModeShownUnderTheInput(t *testing.T) {
	for _, sequence := range []string{"\x1b[Z", "\x1b[9;2u", "\x1b[9;2:1u", "\x1b[27;2;9~"} {
		if events := newChatTerminalKeyParser(1024).feed([]byte(sequence)); len(events) != 1 || !events[0].autoMode {
			t.Fatalf("%q is not Shift+Tab: %+v", sequence, events)
		}
	}
	model := chatViewModel{ControlModel: "GPT-5.5", ControlReasoning: "high"}
	wide := strings.Join(renderChatView(model, 120, 30, menuColorsForTerminal(), false).Lines, "\n")
	if !strings.Contains(wide, "⏵⏵ Auto mode on (Shift+Tab)") {
		t.Fatalf("auto mode control missing:\n%s", wide)
	}
	model.ControlAutoOff = true
	frame := renderChatView(model, 60, 30, menuColorsForTerminal(), false)
	row := -1
	for i, line := range frame.Lines {
		if strings.Contains(line, "◈ GPT-5.5") {
			row = i
		}
	}
	if row < 0 || !strings.Contains(frame.Lines[row], "◇ Auto mode off (Shift+Tab)") {
		t.Fatalf("auto mode off belongs beside the model on narrow controls:\n%s", strings.Join(frame.Lines, "\n"))
	}
	if frame.target(row, strings.Index(frame.Lines[row], "◇")+1) != "auto" {
		t.Fatalf("auto mode control is not tappable: %+v", frame.Targets)
	}
}

func TestChatAutoModeApprovesAfterTimeout(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.busy = true
	request := func(id string) {
		t.Helper()
		if err := u.event(appMessage{ID: json.RawMessage(`"` + id + `"`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"itemId":"cmd","command":"rm -rf build"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	request("a1")
	request("a2")
	if !strings.Contains(out.String(), "Auto mode allows it after 3 minutes") {
		t.Fatalf("approval hint: %q", out.String())
	}
	start := u.approvalDeadline.Add(-chatAutoApproveDelay)
	if got := u.approvalCountdown(start.Add(time.Second)); got != "Auto-allow in 2:59" {
		t.Fatalf("countdown %q", got)
	}
	if answered, err := u.autoApproveExpired(start.Add(chatAutoApproveDelay - time.Second)); answered || err != nil || wire.Len() != 0 {
		t.Fatalf("approved early: %v %v %s", answered, err, wire.String())
	}
	later := start.Add(chatAutoApproveDelay + time.Second)
	if answered, err := u.autoApproveExpired(later); !answered || err != nil {
		t.Fatalf("not approved: %v %v", answered, err)
	}
	if !strings.Contains(wire.String(), `"id":"a1"`) || !strings.Contains(wire.String(), `"decision":"accept"`) || len(u.approvals) != 1 {
		t.Fatalf("reply %s, %d left", wire.String(), len(u.approvals))
	}
	// The next approval gets its own full countdown.
	if !u.approvalDeadline.After(time.Now().Add(chatAutoApproveDelay - time.Minute)) {
		t.Fatalf("next deadline %v", u.approvalDeadline)
	}
	// Turning auto mode off stops the countdown; nothing is approved.
	u.toggleAutoMode()
	wire.Reset()
	if answered, _ := u.autoApproveExpired(later.Add(time.Hour)); answered || wire.Len() != 0 || u.approvalCountdown(later) != "" {
		t.Fatal("auto mode off must wait for an explicit answer")
	}
}

func TestChatApprovalRowShowsCountdown(t *testing.T) {
	frame := renderChatView(chatViewModel{ApprovalPending: true, ApprovalCountdown: "Auto-allow in 2:59"}, 80, 24, menuPalette{}, false)
	if !strings.Contains(strings.Join(frame.Lines, "\n"), "[ n Deny ]  Auto-allow in 2:59") {
		t.Fatalf("approval row:\n%s", strings.Join(frame.Lines, "\n"))
	}
}

func TestChatApprovalAnswersOnEnterOnlyInEmptyBox(t *testing.T) {
	u := &chatUI{approvals: []appMessage{{ID: json.RawMessage(`"a"`)}}}
	editor := newChatEditor(1024)
	if got := chatApprovalKey(u, editor, chatEditorInput{Key: chatEditorEnter}); got != "/approve" {
		t.Fatalf("Enter answered %q", got)
	}
	if got := chatApprovalKey(u, editor, chatEditorInput{Key: chatEditorText, Text: "n"}); got != "/deny" {
		t.Fatalf("n answered %q", got)
	}
	editor.Apply(chatEditorInput{Key: chatEditorText, Text: "wait"})
	if got := chatApprovalKey(u, editor, chatEditorInput{Key: chatEditorEnter}); got != "" {
		t.Fatalf("Enter with a draft must send it, got %q", got)
	}
	u.approvals = nil
	if got := chatApprovalKey(u, newChatEditor(1024), chatEditorInput{Key: chatEditorEnter}); got != "" {
		t.Fatalf("Enter without an approval answered %q", got)
	}
}

func TestChatApprovalCountdownFitsPhones(t *testing.T) {
	frame := renderChatView(chatViewModel{ApprovalPending: true, ApprovalCountdown: "Auto-allow in 2:59"}, 44, 14, menuPalette{}, false)
	screen := strings.Join(frame.Lines, "\n")
	if !strings.Contains(screen, "[ n Deny ]  2:59") || strings.Contains(screen, "Auto-allow") {
		t.Fatalf("phone approval row:\n%s", screen)
	}
}
