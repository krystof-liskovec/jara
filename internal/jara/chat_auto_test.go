package jara

import (
	"bytes"
	"strings"
	"testing"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
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
