package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func chatAlreadyInitialized(message string) bool {
	return strings.Contains(strings.ToLower(message), "already initialized")
}

// recoverChatRequestError handles definitive app-server rejections for
// requests whose failure leaves the current conversation usable.
func recoverChatRequestError(u *chatUI, pending chatPending, message appMessage) bool {
	if message.Error == nil {
		return false
	}
	return u.recoverChatRequest(pending, strings.TrimSpace(chatText(message.Error.Message)))
}

// recoverChatRequest keeps the session usable after a request failed or got
// no reply. Only a failed initialize, which leaves nothing to work with,
// ends the session; other failures are reported in the transcript.
func (u *chatUI) recoverChatRequest(pending chatPending, detail string) bool {
	if detail == "" {
		detail = "the app-server rejected the request"
	}
	switch pending.method {
	case "initialize":
		return false
	case protocol.TurnSelectionSet:
		u.rejectChatTurnSelection(pending.action, detail)
		return true
	case "turn/start":
		u.resetFailedTurn()
		u.setAgentState("idle")
		u.print("\nCould not start turn: " + detail + "\nYou> ")
		return true
	case "thread/start", "thread/resume":
		u.resetFailedTurn()
		if !u.threadReady {
			u.thread = ""
		}
		u.setAgentState("idle")
		operation := "resume conversation"
		if pending.method == "thread/start" {
			operation = "start conversation"
		}
		u.print("\nCould not " + operation + ": " + detail + "\n")
		if chatWriterConflict(detail) {
			u.print(chatWriterConflictHint)
		}
		if u.threadReady {
			u.print("Continuing previous conversation.\nYou> ")
		} else {
			u.print("Use /resume ID or /new to open a conversation.\n")
		}
		return true
	case "turn/interrupt":
		u.cancel = false
		u.setActivityLabel("Thinking")
		u.printRole("system", "Could not stop the turn: "+detail+". Try /cancel again.\n")
		return true
	default:
		u.printRole("system", "Request "+pending.method+" failed: "+detail+"\n")
		return true
	}
}

// expireChatRequest handles a request that got no reply in time.
func (u *chatUI) expireChatRequest(id string, pending chatPending) error {
	switch {
	case u.expireChatDefaultRequest(id, pending), u.expireChatExtraRequest(id, pending), u.expireChatAgentRequest(id, pending):
		return nil
	case pending.method == "thread/list":
		u.expireChatSessionRequest(id)
		return nil
	case pending.method == "model/list":
		u.expireChatModelRequest(id)
		return nil
	}
	delete(u.pending, id)
	delete(u.pendingTurnModels, id)
	if !u.recoverChatRequest(pending, "no reply from the app-server in time") {
		return fmt.Errorf("Codex app-server timed out during %s", pending.method)
	}
	return nil
}

func (u *chatUI) resetFailedTurn() {
	u.busy = false
	u.turn = ""
	u.turnStartedAt = time.Time{}
	u.setActivityLabel("")
	u.cancel = false
	u.approvals = nil
	u.questionRequests = nil
	u.items = map[string]json.RawMessage{}
}
