package jara

import (
	"bytes"
	"testing"
	"time"
)

func TestReasoningSummaryPartsStayInTerminalActivity(t *testing.T) {
	out := &chatTerminalOutput{}
	out.SetChatAgent("gpt-6.2-sol Medium")
	out.WriteChatRole("user", "Review the tests.\n")
	u := &chatUI{out: out, busy: true, activityLabel: "Thinking"}
	u.streamReasoningSummary("summary", 0, "Checking test coverage")
	u.streamReasoningSummary("summary", 1, "Reading the failing test")
	u.finishReasoningSummary("summary", nil)

	if label, _ := chatTerminalActivity(u, time.Now()); label != "Reading the failing test" {
		t.Fatalf("activity label = %q", label)
	}
	for _, line := range out.transcriptLines(80) {
		if line.role == "progress" {
			t.Fatalf("reasoning summary added a transcript row: %q", line.text)
		}
	}

	var plain bytes.Buffer
	u.out = &plain
	u.streamReasoningSummary("summary", 0, "Checking test coverage")
	u.streamReasoningSummary("summary", 1, "Reading the failing test")
	u.finishReasoningSummary("summary", nil)
	if got := plain.String(); got != "Checking test coverage\nReading the failing test\n" {
		t.Fatalf("plain summary = %q", got)
	}
}
