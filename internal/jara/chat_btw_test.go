package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAgentRouterAnswersCodexSideQuestionsInEphemeralFork(t *testing.T) {
	setupFakeAgents(t)
	h := startRouterHarness(t, agentCodex)
	h.response(h.request("initialize", map[string]any{}))
	started := h.response(h.request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	id := h.request("jara/btw", map[string]any{"threadId": thread, "question": "what file?"})
	answer, seen := h.waitFor(func(m map[string]any) bool { v, ok := m["id"].(float64); return ok && v == id })
	if got := answer["result"].(map[string]any)["answer"]; got != "codex: what file?" {
		t.Fatalf("side answer %v", got)
	}
	for _, m := range seen {
		if m["method"] != nil {
			t.Fatalf("fork notification leaked to the UI: %v", m)
		}
	}
	if fork := readFakeLog(t, "codex-fork"); len(fork) != 1 || fork[0] != thread+"|true|read-only" {
		t.Fatalf("fork request %q", fork)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if released := readFakeLog(t, "codex-unsubscribe"); len(released) == 1 && released[0] == "codex-fork" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the side-question fork was not released")
		}
	}
}

func TestAgentRouterAnswersClaudeSideQuestionsWithoutTools(t *testing.T) {
	setupFakeAgents(t)
	h := startRouterHarness(t, agentClaude)
	h.response(h.request("initialize", map[string]any{}))
	started := h.response(h.request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	h.request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "hi"}}})
	h.waitFor(isMethod("turn/completed"))
	reply := h.response(h.request("jara/btw", map[string]any{"threadId": thread, "question": "why?"}))
	if got := reply["result"].(map[string]any)["answer"]; got != "claude btw: why?" {
		t.Fatalf("side answer %v", reply)
	}
	args := readFakeLog(t, "claude-btw")
	if len(args) != 1 || !strings.Contains(args[0], "--tools  ") || !strings.Contains(args[0], "--resume "+thread+" --fork-session") {
		t.Fatalf("Claude side question must fork the session without tools: %q", args)
	}
}

func TestChatBtwCommandShowsAnswerOutsideTheConversation(t *testing.T) {
	t.Setenv("TMUX", "")
	var output, sent bytes.Buffer
	u := &chatUI{out: &output, c: &appConnection{encoder: json.NewEncoder(&sent)}, thread: "t1", threadReady: true, busy: true}
	if err := u.runChatBtwCommand("/btw what now?"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.String(), `"method":"jara/btw"`) || !strings.Contains(sent.String(), `"question":"what now?"`) {
		t.Fatalf("request %s", sent.String())
	}
	u.acceptChatBtwResponse(appMessage{Result: json.RawMessage(`{"answer":"Keep going."}`)})
	if !strings.Contains(output.String(), "Keep going.") || !u.busy {
		t.Fatalf("answer %q", output.String())
	}
	if !isChatBtwCommand("/btw x") || isChatBtwCommand("/btwx") {
		t.Fatal("command detection")
	}
}
