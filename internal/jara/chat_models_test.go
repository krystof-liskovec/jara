package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type modelWireRequest struct {
	ID     json.RawMessage            `json:"id"`
	Method string                     `json:"method"`
	Params map[string]json.RawMessage `json:"params"`
}

func modelPageJSON(t *testing.T, models []codexModel, next string) json.RawMessage {
	t.Helper()
	page := struct {
		Data       []codexModel `json:"data"`
		NextCursor *string      `json:"nextCursor,omitempty"`
	}{Data: models}
	if next != "" {
		page.NextCursor = &next
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeModelRequests(t *testing.T, wire *bytes.Buffer) []modelWireRequest {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(wire.Bytes()))
	var requests []modelWireRequest
	for {
		var request modelWireRequest
		if err := decoder.Decode(&request); err != nil {
			break
		}
		requests = append(requests, request)
	}
	return requests
}

func TestChatModelListSchemaParamsAndFrozenChoices(t *testing.T) {
	browser := newChatModelBrowser()
	params := browser.requestParams()
	if params["limit"] != chatModelPageSize || params["includeHidden"] != false {
		t.Fatalf("unexpected model/list params: %#v", params)
	}
	page := modelPageJSON(t, []codexModel{
		{ID: "gpt-6", Model: "gpt-6-codex", DisplayName: "GPT-6 Codex"},
		{ID: "gpt-6-fast", Model: "gpt-6-fast", DisplayName: "GPT-6 Fast"},
		{ID: "hidden", Model: "hidden", Hidden: true},
	}, "")
	more, err := browser.acceptPage(page)
	if err != nil || more || !browser.complete {
		t.Fatalf("catalog page: more=%v complete=%v err=%v", more, browser.complete, err)
	}
	if len(browser.models) != 2 {
		t.Fatalf("hidden models should not be selectable: %+v", browser.models)
	}
	if model, label, err := browser.model("1"); err != nil || model != "gpt-6-codex" || label != "GPT-6 Codex" {
		t.Fatalf("numeric selection: model=%q label=%q err=%v", model, label, err)
	}
	if model, label, err := browser.model("gpt-6-fast"); err != nil || model != "gpt-6-fast" || label != "GPT-6 Fast" {
		t.Fatalf("ID selection: model=%q label=%q err=%v", model, label, err)
	}
	if !validChatModelID("provider:gpt-6/model") {
		t.Fatal("provider-qualified model ID should be accepted")
	}
	if validChatModelID("model id") || validChatModelID("model\x1b[31m") {
		t.Fatal("model IDs must not contain whitespace or control characters")
	}
}

func TestChatModelListRejectsMissingData(t *testing.T) {
	for _, raw := range []string{`{}`, `{"data":null}`} {
		browser := newChatModelBrowser()
		if _, err := browser.acceptPage(json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), "missing data") {
			t.Errorf("invalid model page %s: error=%v", raw, err)
		}
	}
}

func TestChatModelsAreAsyncAndSelectionChangesNextTurnOnlyLocally(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.connected = true
	u.cwd = "/workspace"
	u.effectiveModel = "configured-default"
	if _, err := u.line("/models"); err != nil {
		t.Fatal(err)
	}
	requests := decodeModelRequests(t, &wire)
	if len(requests) != 1 || requests[0].Method != "model/list" {
		t.Fatalf("/models did not queue an async app-server request: %+v", requests)
	}
	if len(u.pending) != 1 || u.busy {
		t.Fatalf("model listing blocked or changed chat state: pending=%v busy=%v", u.pending, u.busy)
	}
	page := modelPageJSON(t, []codexModel{
		{ID: "gpt-6", Model: "gpt-6", DisplayName: "GPT-6"},
		{ID: "gpt-6-codex", Model: "gpt-6-codex", DisplayName: "GPT-6 Codex"},
	}, "")
	if err := u.response(appMessage{ID: requests[0].ID, Result: page}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("/model 2"); err != nil {
		t.Fatal(err)
	}
	if u.effectiveModel != "configured-default" || u.nextModel != "gpt-6-codex" {
		t.Fatalf("selection should be queued separately from effective model: current=%q next=%q", u.effectiveModel, u.nextModel)
	}
	if !strings.Contains(u.status(), "Model: configured-default") || !strings.Contains(u.status(), "Next turns model: GPT-6 Codex") {
		t.Fatalf("status did not distinguish current and selected models: %q", u.status())
	}
	if _, err := u.line("Use selected model"); err != nil {
		t.Fatal(err)
	}
	requests = decodeModelRequests(t, &wire)
	var firstTurn modelWireRequest
	for _, request := range requests {
		if request.Method == "turn/start" {
			firstTurn = request
			break
		}
	}
	var selected string
	if err := json.Unmarshal(firstTurn.Params["model"], &selected); err != nil || selected != "gpt-6-codex" {
		t.Fatalf("selected model missing from next turn request: %s", string(firstTurn.Params["model"]))
	}
	if err := u.response(appMessage{ID: firstTurn.ID, Result: json.RawMessage(`{"turn":{"id":"turn-1"}}`)}); err != nil {
		t.Fatal(err)
	}
	if u.effectiveModel != "gpt-6-codex" || u.nextModel != "" {
		t.Fatalf("accepted model override was not confirmed: current=%q next=%q", u.effectiveModel, u.nextModel)
	}
	if err := u.event(appMessage{Method: "turn/completed", Params: json.RawMessage(`{"turn":{"id":"turn-1","status":"completed"}}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("Use current model again"); err != nil {
		t.Fatal(err)
	}
	requests = decodeModelRequests(t, &wire)
	turnCount := 0
	for _, request := range requests {
		if request.Method == "turn/start" {
			turnCount++
			if turnCount == 2 {
				if _, has := request.Params["model"]; has {
					t.Fatalf("subsequent turn should inherit thread model without another override: %s", string(request.Params["model"]))
				}
			}
		}
	}
	if turnCount != 2 {
		t.Fatalf("expected two turn requests, got %d", turnCount)
	}
}

func TestChatModelDefaultTurnOmitsOverride(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	if err := u.sendPrompt("use configured model"); err != nil {
		t.Fatal(err)
	}
	requests := decodeModelRequests(t, &wire)
	if len(requests) != 1 || requests[0].Method != "turn/start" {
		t.Fatalf("unexpected request: %+v", requests)
	}
	if _, has := requests[0].Params["model"]; has {
		t.Fatalf("default turn should preserve Codex config without model override: %s", string(requests[0].Params["model"]))
	}
}

func TestChatModelSelectionBusyRejectionAndFailedTurnPreservation(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.effectiveModel = "current"
	u.busy = true
	u.turn = "turn-live"
	if _, err := u.line("/model next-model"); err != nil {
		t.Fatal(err)
	}
	if u.nextModel != "" || wire.Len() != 0 || !strings.Contains(output.String(), "wait for the current turn") {
		t.Fatalf("busy model selection was not rejected: next=%q wire=%q output=%q", u.nextModel, wire.String(), output.String())
	}
	u.busy = false
	u.turn = ""
	if _, err := u.line("/model next-model"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("try selected model"); err != nil {
		t.Fatal(err)
	}
	requests := decodeModelRequests(t, &wire)
	if len(requests) != 1 || requests[0].Method != "turn/start" {
		t.Fatalf("unexpected turn request: %+v", requests)
	}
	var model string
	if err := json.Unmarshal(requests[0].Params["model"], &model); err != nil || model != "next-model" {
		t.Fatalf("selected model was not sent: %s", string(requests[0].Params["model"]))
	}
	if err := u.response(appMessage{ID: requests[0].ID, Error: &struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}{Message: "model unavailable"}}); err != nil {
		t.Fatal(err)
	}
	if u.busy || u.effectiveModel != "current" || u.nextModel != "next-model" {
		t.Fatalf("rejected request should preserve old effective and selected models: busy=%v effective=%q next=%q", u.busy, u.effectiveModel, u.nextModel)
	}
}

func TestChatModelListFailureAndResumeModel(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.connected = true
	u.cwd = "/workspace"
	_, _ = u.line("/models")
	requests := decodeModelRequests(t, &wire)
	if err := u.response(appMessage{ID: requests[0].ID, Error: &struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}{Message: "catalog unavailable"}}); err != nil {
		t.Fatalf("model/list error should not close the chat: %v", err)
	}
	if !strings.Contains(output.String(), "Model lookup failed: Codex app-server rejected model/list: catalog unavailable") || u.busy {
		t.Fatalf("model/list failure was not nonfatal and visible: busy=%v output=%q", u.busy, output.String())
	}
	if err := u.open("thread-resumed"); err != nil {
		t.Fatal(err)
	}
	requests = decodeModelRequests(t, &wire)
	resumeRequest := requests[len(requests)-1]
	if resumeRequest.Method != "thread/resume" {
		t.Fatalf("unexpected resume request: %+v", resumeRequest)
	}
	if err := u.response(appMessage{ID: resumeRequest.ID, Result: json.RawMessage(`{"model":"gpt-6-codex","thread":{"id":"thread-resumed","turns":[]}}`)}); err != nil {
		t.Fatal(err)
	}
	if u.effectiveModel != "gpt-6-codex" || !strings.Contains(u.status(), "Model: gpt-6-codex") {
		t.Fatalf("resume model was not reflected in status: model=%q status=%q", u.effectiveModel, u.status())
	}
}

func TestChatModelStartResponseAfterCompletionConfirmsOnlySentSelection(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output, wire bytes.Buffer
	u := newChatCommandTestUI(&output, &wire)
	u.effectiveModel = "old-model"
	if err := u.chooseChatModel("sent-model"); err != nil {
		t.Fatal(err)
	}
	if err := u.sendPrompt("go"); err != nil {
		t.Fatal(err)
	}
	requests := decodeModelRequests(t, &wire)
	turn := requests[0]
	if err := u.event(appMessage{Method: "turn/completed", Params: json.RawMessage(`{"turn":{"id":"turn-1","status":"completed"}}`)}); err != nil {
		t.Fatal(err)
	}
	if err := u.chooseChatModel("new-selection"); err != nil {
		t.Fatal(err)
	}
	if err := u.response(appMessage{ID: turn.ID, Result: json.RawMessage(`{"turn":{"id":"turn-1"}}`)}); err != nil {
		t.Fatal(err)
	}
	if u.effectiveModel != "sent-model" || u.nextModel != "new-selection" {
		t.Fatalf("late response should confirm its model without clearing newer selection: effective=%q next=%q", u.effectiveModel, u.nextModel)
	}
}

func TestLateTurnStartCannotChangeModelOrStateAfterThreadSwitch(t *testing.T) {
	for _, lateError := range []bool{false, true} {
		name := "late-success"
		if lateError {
			name = "late-error"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMUX", "")
			t.Setenv("TMUX_PANE", "")
			var output, wire bytes.Buffer
			u := newChatCommandTestUI(&output, &wire)
			u.thread = "thread-old"
			u.effectiveModel = "model-old"
			if err := u.sendPrompt("old thread turn"); err != nil {
				t.Fatal(err)
			}
			requests := decodeModelRequests(t, &wire)
			turnRequest := requests[0]
			if err := u.event(appMessage{Method: "turn/completed", Params: json.RawMessage(`{"turn":{"id":"turn-old","status":"completed"}}`)}); err != nil {
				t.Fatal(err)
			}
			if err := u.open("thread-new"); err != nil {
				t.Fatal(err)
			}
			requests = decodeModelRequests(t, &wire)
			resumeRequest := requests[len(requests)-1]
			if err := u.response(appMessage{ID: resumeRequest.ID, Result: json.RawMessage(`{"model":"model-new","thread":{"id":"thread-new","turns":[]}}`)}); err != nil {
				t.Fatal(err)
			}
			if u.thread != "thread-new" || u.effectiveModel != "model-new" || u.busy {
				t.Fatalf("new thread did not become active: thread=%q model=%q busy=%v", u.thread, u.effectiveModel, u.busy)
			}
			late := appMessage{ID: turnRequest.ID}
			if lateError {
				late.Error = &struct {
					Message string          `json:"message"`
					Data    json.RawMessage `json:"data"`
				}{Message: "old thread failed"}
			} else {
				late.Result = json.RawMessage(`{"turn":{"id":"turn-old"}}`)
			}
			if err := u.response(late); err != nil {
				t.Fatalf("late old-thread result should be ignored: %v", err)
			}
			if u.thread != "thread-new" || u.effectiveModel != "model-new" || u.busy || u.turn != "" {
				t.Fatalf("late turn response changed current thread: thread=%q model=%q busy=%v turn=%q", u.thread, u.effectiveModel, u.busy, u.turn)
			}
		})
	}
}
