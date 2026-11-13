package jara

import (
	"bytes"
	"strings"
	"testing"

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
		if strings.Contains(line, "◈ Model") {
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
