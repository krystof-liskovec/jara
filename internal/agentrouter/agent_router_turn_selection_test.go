package agentrouter

import (
	"bytes"
	"encoding/json"
	"testing"

	"krystof.liskovec/jara/internal/protocol"
)

func TestTurnSelectionAppliesWhenTheNextQueuedTurnStarts(t *testing.T) {
	var input, output bytes.Buffer
	thread := &Thread{ID: "thread", CodexModel: "gpt-current", CodexEffort: "low",
		Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: "thread"}}}
	router := &agentRouter{codexIn: json.NewEncoder(&input), out: &output,
		codexCalls: map[string]codexCall{}, threads: map[string]*Thread{thread.ID: thread}}
	set := func(model, effort string) {
		t.Helper()
		params, _ := json.Marshal(protocol.TurnSelectionUpdate{
			ThreadID: thread.ID, Next: protocol.TurnSelection{Model: model, Effort: effort},
		})
		if err := router.fromUI(rpcMessage{ID: json.RawMessage("1"), Method: protocol.TurnSelectionSet, Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	startQueued := func(id, model, effort string) {
		t.Helper()
		if err := router.startTurn(json.RawMessage(id), json.RawMessage(`{"threadId":"thread","input":[{"type":"text","text":"queued earlier"}]}`)); err != nil {
			t.Fatal(err)
		}
		var request rpcMessage
		if err := json.NewDecoder(&input).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var got protocol.TurnSelection
		if err := json.Unmarshal(request.Params, &got); err != nil {
			t.Fatal(err)
		}
		if got != (protocol.TurnSelection{Model: model, Effort: effort}) || !bytes.Contains(request.Params, []byte("queued earlier")) {
			t.Fatalf("queued turn params = %s", request.Params)
		}
	}

	set("gpt-next", "high")
	if input.Len() != 0 || thread.CodexModel != "gpt-current" || thread.CodexEffort != "low" {
		t.Fatal("staging the choice changed the current turn")
	}
	startQueued(`"jara-schedule-start-q-one"`, "gpt-next", "high")
	set("gpt-later", "xhigh")
	if err := router.fromCodex(rpcMessage{ID: json.RawMessage(`"jara-schedule-start-q-one"`), Result: json.RawMessage(`{"turn":{"id":"one"}}`)}); err != nil {
		t.Fatal(err)
	}
	startQueued(`"jara-schedule-start-q-two"`, "gpt-later", "xhigh")
	if err := router.fromCodex(rpcMessage{ID: json.RawMessage(`"jara-schedule-start-q-two"`), Error: json.RawMessage(`{"message":"unavailable"}`)}); err != nil {
		t.Fatal(err)
	}
	startQueued(`"jara-schedule-start-q-three"`, "gpt-later", "xhigh")
	if err := router.fromCodex(rpcMessage{ID: json.RawMessage(`"jara-schedule-start-q-three"`), Result: json.RawMessage(`{"turn":{"id":"three"}}`)}); err != nil {
		t.Fatal(err)
	}
	if thread.NextTurn != nil || !bytes.Contains(output.Bytes(), []byte(`"applied":{"model":"gpt-later","effort":"xhigh"}`)) {
		t.Fatalf("accepted settings were not announced and cleared: %s", output.String())
	}
	startQueued(`"jara-schedule-start-q-four"`, "", "")
}
