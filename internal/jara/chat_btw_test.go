package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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
