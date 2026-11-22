package jara

import (
	"strings"
	"testing"
	"time"
)

func TestQueuedPromptsShowUnderOneHeader(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	term.u.threadReady, term.u.busy, term.u.turn = true, true, "turn-1"
	for _, prompt := range []string{"first prompt", "second prompt"} {
		if err := term.u.queueLocalPrompt(term.u.thread, prompt); err != nil {
			t.Fatal(err)
		}
	}
	screen := chatScreenText(term.frame(100, 30, time.Now()))
	if strings.Count(screen, "Queued") != 1 || !strings.Contains(screen, "Queued · 2 · start when idle") {
		t.Fatalf("one header counts the queue:\n%s", screen)
	}
	first, second := strings.Index(screen, " 1  first prompt"), strings.Index(screen, " 2  second prompt")
	if first < 0 || second < first {
		t.Fatalf("prompts are numbered in the order they run:\n%s", screen)
	}

	term.queueEditor.id = term.u.queuedPrompts[1].id
	lines := term.u.queueViewLines(term.queueEditor.id)
	if lines[0].kind != "queued-prompt-header" || lines[2].kind != "queued-prompt-edit" || !strings.HasPrefix(lines[2].text, " › 2") {
		t.Fatalf("the edited prompt is marked: %+v", lines)
	}
}

func TestQueueWindowKeepsTheHeaderWhenShort(t *testing.T) {
	lines := []chatViewLine{{text: "Queued · 3", kind: "queued-prompt-header"}, {text: "1"}, {text: "2"}, {text: "3"}}
	if got := chatQueueWindow(lines, 1); len(got) != 1 || got[0].text != "Queued · 3" {
		t.Fatalf("one row shows only the header: %+v", got)
	}
	got := chatQueueWindow(lines, 3)
	if len(got) != 3 || got[0].kind != "queued-prompt-header" || got[1].text != "1" || !strings.HasPrefix(got[2].text, "… 2 more") {
		t.Fatalf("overflow keeps the header and counts hidden prompts: %+v", got)
	}
	if got := chatQueueWindow(nil, 3); got != nil {
		t.Fatalf("an empty queue takes no rows: %+v", got)
	}
}
