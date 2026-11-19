package jara

import (
	"encoding/json"
	"fmt"

	"krystof.liskovec/jara/internal/protocol"
)

func (u *chatUI) publishChatTurnSelection(model, effort string) error {
	if !u.sharedTurnSelection {
		return nil
	}
	if u.chatOffline() {
		return fmt.Errorf("wait for Jara to reconnect before changing the next turn's settings")
	}
	return u.requestWithAction(protocol.TurnSelectionSet, protocol.TurnSelectionUpdate{
		ThreadID: u.thread, Next: protocol.TurnSelection{Model: model, Effort: effort},
	}, u.thread)
}

func (u *chatUI) loadChatTurnSelection() {
	if u.sharedTurnSelection {
		_ = u.request(protocol.TurnSelectionGet, protocol.TurnSelectionUpdate{ThreadID: u.thread})
	}
}

func (u *chatUI) rejectChatTurnSelection(thread, detail string) {
	if thread != u.thread {
		return
	}
	u.printRole("system", "Could not change the next turn's settings: "+detail+"\n")
	u.loadChatTurnSelection()
}

func (u *chatUI) acceptChatTurnSelection(raw json.RawMessage) {
	var update protocol.TurnSelectionUpdate
	if json.Unmarshal(raw, &update) != nil || update.ThreadID != u.thread {
		return
	}
	if applied := update.Applied; applied != nil {
		if applied.Model != "" {
			u.setEffectiveModel(applied.Model)
			u.effectiveReasoning = ""
			if model := u.modelByID(applied.Model); model != nil {
				u.effectiveReasoning = model.DefaultReasoningEffort
			}
		}
		if applied.Effort != "" {
			u.effectiveReasoning = applied.Effort
		}
		u.labelReplies()
	}
	for _, p := range u.pending {
		if p.method == protocol.TurnSelectionSet && p.action == u.thread {
			return // an earlier acknowledgement must not replace a newer selection
		}
	}
	u.nextModel, u.nextReasoning = chatText(update.Next.Model), chatText(update.Next.Effort)
	u.nextModelLabel = ""
	if u.nextModel != "" {
		u.nextModelLabel = u.modelName(u.nextModel)
	}
}
