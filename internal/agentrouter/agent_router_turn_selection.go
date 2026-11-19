package agentrouter

import (
	"encoding/json"

	"krystof.liskovec/jara/internal/protocol"
)

type turnSelectionRequest struct {
	thread    *Thread
	selection *protocol.TurnSelection
}

func (r *agentRouter) handleTurnSelection(id json.RawMessage, method string, params json.RawMessage) {
	var update protocol.TurnSelectionUpdate
	if json.Unmarshal(params, &update) != nil || r.threads[update.ThreadID] == nil {
		r.fail(id, "open the conversation before changing its turn settings")
		return
	}
	thread := r.threads[update.ThreadID]
	if method == protocol.TurnSelectionGet {
		r.reply(id, thread.turnSelectionUpdate())
		return
	}
	thread.NextTurn = &update.Next
	thread.Save()
	r.reply(id, map[string]any{})
	r.notify(protocol.TurnSelectionUpdated, thread.turnSelectionUpdate())
}

func (t *Thread) turnSelectionUpdate() protocol.TurnSelectionUpdate {
	update := protocol.TurnSelectionUpdate{ThreadID: t.ID}
	if t.NextTurn != nil {
		update.Next = *t.NextTurn
	}
	return update
}

func (r *agentRouter) applyTurnSelection(id json.RawMessage, thread *Thread, params json.RawMessage, explicit protocol.TurnSelection) (json.RawMessage, protocol.TurnSelection) {
	selection := thread.NextTurn
	if selection == nil || explicit != (protocol.TurnSelection{}) {
		return params, explicit
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil || fields == nil {
		return params, explicit
	}
	if selection.Model != "" {
		fields["model"], _ = json.Marshal(selection.Model)
	}
	if selection.Effort != "" {
		fields["effort"], _ = json.Marshal(selection.Effort)
	}
	params, _ = json.Marshal(fields)
	if r.turnSelections == nil {
		r.turnSelections = map[string]turnSelectionRequest{}
	}
	r.turnSelections[string(id)] = turnSelectionRequest{thread: thread, selection: selection}
	return params, *selection
}

func (r *agentRouter) finishTurnSelection(id json.RawMessage, success bool) {
	request, ok := r.turnSelections[string(id)]
	if !ok {
		return
	}
	delete(r.turnSelections, string(id))
	if !success {
		return
	}
	thread := request.thread
	if thread.NextTurn == request.selection {
		thread.NextTurn = nil
		thread.Save()
	}
	update := thread.turnSelectionUpdate()
	update.Applied = request.selection
	r.notify(protocol.TurnSelectionUpdated, update)
}
