package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

func questionEvent(id, questionJSON string) appMessage {
	return appMessage{
		ID:     json.RawMessage(id),
		Method: "item/tool/requestUserInput",
		Params: json.RawMessage(`{"isBlocking":true,"itemId":"item-1","questions":` + questionJSON + `,"threadId":"thread-1","turnId":"turn-1"}`),
	}
}

func newQuestionTestUI() (*chatUI, *bytes.Buffer, *bytes.Buffer) {
	var output, wire bytes.Buffer
	c := &appConnection{encoder: json.NewEncoder(&wire)}
	u := &chatUI{c: c, out: &output, busy: true, pending: map[string]chatPending{}, items: map[string]json.RawMessage{}}
	return u, &output, &wire
}

func TestChatQuestionPromptAndAnswers(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	u, output, wire := newQuestionTestUI()
	questions := `[{"header":"Choose a color","id":"color","question":"Which color should be used for the interface? 界面 🚀","options":[{"label":"Red","description":"Warm and vivid"},{"label":"Blue","description":"Cool and calm with a deliberately long description that wraps across lines"}]},{"header":"Summary","id":"summary","question":"What should the final note say?","options":[]}]`
	if err := u.event(questionEvent(`"request-17"`, questions)); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 {
		t.Fatalf("question request was answered before user input: %s", wire.String())
	}
	if u.currentState() != "waiting" || u.pendingQuestionCount() != 2 {
		t.Fatalf("question state: %s, pending=%d", u.currentState(), u.pendingQuestionCount())
	}
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if width := uniseg.StringWidth(line); width > chatQuestionLineWidth {
			t.Errorf("question line is %d columns wide: %q", width, line)
		}
	}
	if !strings.Contains(output.String(), "2) Blue — Cool and calm") {
		t.Fatalf("options were not numbered and shown: %s", output.String())
	}
	if _, err := u.line("/answer 1 2"); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || u.pendingQuestionCount() != 1 {
		t.Fatalf("request replied before all questions were answered: wire=%s pending=%d", wire.String(), u.pendingQuestionCount())
	}
	if _, err := u.line("/answer 2 write a short, clear note"); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(wire.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if string(response.ID) != `"request-17"` {
		t.Fatalf("request ID changed: %s", wire.String())
	}
	if got := response.Result.Answers["color"].Answers; len(got) != 1 || got[0] != "Blue" {
		t.Fatalf("option answer = %q", got)
	}
	if got := response.Result.Answers["summary"].Answers; len(got) != 1 || got[0] != "write a short, clear note" {
		t.Fatalf("free-text answer = %q", got)
	}
}

func TestChatQuestionRequestsQueueAndPreserveIDs(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	u, _, wire := newQuestionTestUI()
	if err := u.event(questionEvent(`"request-first"`, `[{"header":"First","id":"q-first","question":"Tell me more"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := u.event(questionEvent(`42`, `[{"header":"Second","id":"q-second","question":"Pick one","options":[{"label":"A","description":"Option A"}]}]`)); err != nil {
		t.Fatal(err)
	}
	if len(u.questionRequests) != 2 || u.pendingQuestionCount() != 2 {
		t.Fatalf("requests not queued: requests=%d questions=%d", len(u.questionRequests), u.pendingQuestionCount())
	}
	if _, err := u.line("/answer 1 first answer"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("/answer 1 1"); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(wire)
	var first, second struct {
		ID json.RawMessage `json:"id"`
	}
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if string(first.ID) != `"request-first"` || string(second.ID) != `42` {
		t.Fatalf("request IDs not preserved: first=%s second=%s", first.ID, second.ID)
	}
	if len(u.questionRequests) != 0 || u.currentState() != "running" {
		t.Fatalf("question queue not drained: state=%s pending=%d", u.currentState(), len(u.questionRequests))
	}
}

func TestChatSecretQuestionIsRejectedWithoutDisplay(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	u, output, wire := newQuestionTestUI()
	question := `[{"header":"Password","id":"secret","question":"Enter the secret credential","isSecret":true}]`
	if err := u.event(questionEvent(`"secret-request"`, question)); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(wire.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if string(response.ID) != `"secret-request"` || response.Error.Code != chatQuestionInvalidCode {
		t.Fatalf("secret request not explicitly rejected: %s", wire.String())
	}
	if strings.Contains(output.String(), "Enter the secret credential") || len(u.questionRequests) != 0 {
		t.Fatalf("secret question was displayed or collected: %q", output.String())
	}
}

func TestChatCancelAndQuitRejectPendingQuestions(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Run("cancel", func(t *testing.T) {
		u, _, wire := newQuestionTestUI()
		u.turn = "turn-1"
		for _, id := range []string{`"one"`, `"two"`} {
			if err := u.event(questionEvent(id, `[{"header":"Name","id":"name","question":"What is it?"}]`)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := u.line("/cancel"); err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(wire)
		for _, wantID := range []string{`"one"`, `"two"`} {
			var response struct {
				ID    json.RawMessage `json:"id"`
				Error struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err := decoder.Decode(&response); err != nil {
				t.Fatal(err)
			}
			if string(response.ID) != wantID || response.Error.Code != chatQuestionCancelledCode {
				t.Fatalf("question cancellation response: id=%s error=%d", response.ID, response.Error.Code)
			}
		}
		var interrupt struct {
			Method string `json:"method"`
		}
		if err := decoder.Decode(&interrupt); err != nil || interrupt.Method != "turn/interrupt" {
			t.Fatalf("turn interrupt not sent: %+v, %v", interrupt, err)
		}
		if len(u.questionRequests) != 0 {
			t.Fatalf("questions remain after cancel: %d", len(u.questionRequests))
		}
	})
	t.Run("quit", func(t *testing.T) {
		u, _, wire := newQuestionTestUI()
		if err := u.event(questionEvent(`"quit-request"`, `[{"header":"Name","id":"name","question":"What is it?"}]`)); err != nil {
			t.Fatal(err)
		}
		quit, err := u.line("/quit")
		if err != nil || !quit {
			t.Fatalf("quit = %v, %v", quit, err)
		}
		var response struct {
			ID    json.RawMessage `json:"id"`
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(wire.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if string(response.ID) != `"quit-request"` || response.Error.Code != chatQuestionCancelledCode {
			t.Fatalf("quit did not cancel question: %s", wire.String())
		}
	})
}

func TestApprovalResolutionKeepsWaitingQuestionState(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	u, _, wire := newQuestionTestUI()
	if err := u.event(appMessage{ID: json.RawMessage(`"approval"`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"itemId":"cmd","command":"echo ok"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := u.event(questionEvent(`"question"`, `[{"header":"Reason","id":"reason","question":"Why?"}]`)); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("/approve"); err != nil {
		t.Fatal(err)
	}
	if u.currentState() != "waiting" || len(u.questionRequests) != 1 {
		t.Fatalf("approval changed pending-question state: %s", u.currentState())
	}
	var response struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			Decision string `json:"decision"`
		} `json:"result"`
	}
	if err := json.Unmarshal(wire.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if string(response.ID) != `"approval"` || response.Result.Decision != "accept" {
		t.Fatalf("approval reply changed: %s", wire.String())
	}
}
