package jara

import (
	"bytes"
	"encoding/json"
	"testing"

	"krystof.liskovec/jara/internal/protocol"
)

func TestChatSharesSelectionWithAnExistingPersistentQueue(t *testing.T) {
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.persistentSession, u.sharedTurnSelection = true, true
	u.busy, u.turn = true, "active-turn"
	u.effectiveModel, u.effectiveReasoning = "gpt-current", "low"
	u.models = &chatModelBrowser{complete: true, models: []protocol.CodexModel{
		{ID: "gpt-next", DefaultReasoningEffort: "low", SupportedReasoningEfforts: []protocol.ReasoningEffortOption{
			{Value: "low"}, {Value: "high"},
		}},
	}}
	if err := u.queuePrompt(u.thread, "queued before selecting settings"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("/model gpt-next"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.line("/reasoning high"); err != nil {
		t.Fatal(err)
	}
	requests := decodeModelRequests(t, &wire)
	if len(requests) != 3 || requests[0].Method != "jara/queue/add" ||
		requests[1].Method != protocol.TurnSelectionSet || requests[2].Method != protocol.TurnSelectionSet {
		t.Fatalf("unexpected requests: %+v", requests)
	}
	var next protocol.TurnSelection
	if err := json.Unmarshal(requests[2].Params["next"], &next); err != nil || next.Model != "gpt-next" || next.Effort != "high" {
		t.Fatalf("next turn settings = %+v, err = %v", next, err)
	}
	if u.effectiveModel != "gpt-current" || u.effectiveReasoning != "low" || u.turn != "active-turn" {
		t.Fatal("selecting the next turn's settings modified the active turn")
	}
	for _, request := range requests[1:] {
		if err := u.response(appMessage{ID: request.ID, Result: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	update, _ := json.Marshal(protocol.TurnSelectionUpdate{ThreadID: u.thread, Applied: &next})
	if err := u.event(appMessage{Method: protocol.TurnSelectionUpdated, Params: update}); err != nil {
		t.Fatal(err)
	}
	if u.effectiveModel != "gpt-next" || u.effectiveReasoning != "high" || u.nextModel != "" || u.nextReasoning != "" {
		t.Fatalf("accepted settings: model=%q effort=%q next model=%q next effort=%q", u.effectiveModel, u.effectiveReasoning, u.nextModel, u.nextReasoning)
	}
}
