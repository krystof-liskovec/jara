//go:build linux || darwin

package jara

import (
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/protocol"
)

func chatReplyHeaders(out *chatTerminalOutput) []string {
	var headers []string
	for _, line := range out.transcriptLines(80) {
		if line.kind == "role-assistant" {
			headers = append(headers, strings.SplitN(line.text, " · ", 2)[0])
		}
	}
	return headers
}

func TestRepliesNameTheirModelAndReasoning(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	u := term.u
	if got := u.replyLabel("claude/opus", "claude-opus-5-5", "xhigh"); got != "Claude Opus 5.5 Extra high" {
		t.Fatalf("Claude label %q", got)
	}
	if got := u.replyLabel("gpt-5.5", "", ""); got != "gpt-5.5" {
		t.Fatalf("label without reasoning %q", got)
	}

	u.setEffectiveModel("claude/opus")
	u.effectiveReasoning = "xhigh"
	u.labelReplies()
	u.printRole("assistant", "First.\n")
	// Claude Code names its concrete model once it starts.
	u.resolvedModel = "claude-opus-5-5"
	u.labelReplies()
	u.printRole("user", "next\n")
	u.printRole("assistant", "Second.\n")
	want := []string{"✦ Claude Opus Extra high", "✦ Claude Opus 5.5 Extra high"}
	if got := chatReplyHeaders(term.transcript); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("headers %q, want %q", got, want)
	}
}

func TestReplayedRepliesKeepTheModelThatWroteThem(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	u := term.u
	u.setEffectiveModel("claude/opus")
	u.resolvedModel, u.effectiveReasoning = "claude-opus-5-5", "high"
	u.renderChatHistory([]protocol.HistoryTurn{{ID: "t", Items: []protocol.HistoryItem{
		{Type: "agentMessage", Text: "From Codex.", Agent: "codex", Model: "gpt-5.5", Effort: "low"},
		{Type: "agentMessage", Text: "From Claude.", Agent: "claude", Model: "claude/opus", ResolvedModel: "claude-opus-5-5", Effort: "xhigh"},
		{Type: "agentMessage", Text: "Before models were recorded.", Agent: "claude"},
	}}})
	u.printRole("assistant", "Live.\n")
	want := []string{"✦ gpt-5.5 Low", "✦ Claude Opus 5.5 Extra high", "✦ Claude Code", "✦ Claude Opus 5.5 High"}
	if got := chatReplyHeaders(term.transcript); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("headers %q, want %q", got, want)
	}
}
