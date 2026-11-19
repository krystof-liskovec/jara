package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func openTestChatThread(t *testing.T, term *chatTerminal, wire *bytes.Buffer, result string) {
	t.Helper()
	typeChatKeys(t, term, "/new\r")
	requests := decodeModelRequests(t, wire)
	last := requests[len(requests)-1]
	if err := term.handleMessage(appMessage{ID: last.ID, Result: json.RawMessage(result)}); err != nil {
		t.Fatal(err)
	}
}

func TestChatWelcomeShowsRunnerNote(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	term.u.workspaceLabel = "jara"
	term.u.afterHistory = "The agent runs on this device (cachyos); jellyfish keeps every conversation after each turn.\n"
	openTestChatThread(t, term, wire, `{"thread":{"id":"thread-new","turns":[]}}`)

	screen := chatScreenText(term.frame(100, 30, time.Now()))
	for _, want := range []string{"Ready when you are", "explore or change jara.", "jellyfish keeps every", "/model", "/new"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("welcome screen lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "• Jara") {
		t.Fatalf("the note should not open the transcript:\n%s", screen)
	}
}

func TestChatWelcomeNoteJoinsHistoryOfSavedConversation(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	term.u.afterHistory = "The agent runs on this device.\n"
	openTestChatThread(t, term, wire, `{"thread":{"id":"thread-saved","turns":[{"id":"turn-1","items":[{"type":"agentMessage","text":"Earlier reply."}]}]}}`)

	screen := chatScreenText(term.frame(100, 30, time.Now()))
	if !strings.Contains(screen, "• Jara") || !strings.Contains(screen, "The agent runs on this device.") || strings.Contains(screen, "Ready when you are") {
		t.Fatalf("a saved conversation should show the note after its history:\n%s", screen)
	}
}

func TestChatWelcomeHintsFitNarrowScreens(t *testing.T) {
	for _, line := range chatWelcomeTranscript(12, 30, "", chatWelcome{Note: "The agent runs on this device (cachyos); jellyfish keeps every conversation after each turn."}) {
		if (line.kind == "welcome-note" || line.kind == "welcome-hint") && len([]rune(strings.TrimRight(line.text, " "))) > 30 {
			t.Fatalf("line %q is wider than the screen", line.text)
		}
	}
}
