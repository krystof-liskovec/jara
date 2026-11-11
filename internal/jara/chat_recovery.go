package jara

import (
	"encoding/json"
	"strings"
	"time"
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
	detail := strings.TrimSpace(chatText(message.Error.Message))
	if detail == "" {
		detail = "the app-server rejected the request"
	}
	switch pending.method {
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
	default:
		return false
	}
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
