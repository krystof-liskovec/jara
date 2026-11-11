package jara

import "strings"

// chatAutoReviewer is the app-server approvalsReviewer for auto mode. Codex
// routes approval requests to its auto-review subagent; Jara's agent router
// runs Claude Code in its auto permission mode.
const chatAutoReviewer = "auto_review"

// approvalsReviewer is sent with every conversation request so the agent
// uses the mode the user selected. Auto mode is the default.
func (u *chatUI) approvalsReviewer() string {
	if u.manualApprovals {
		return "user"
	}
	return chatAutoReviewer
}

func isChatAutoCommand(line string) bool {
	line = strings.TrimSpace(line)
	return line == "/auto" || strings.HasPrefix(line, "/auto ") || strings.HasPrefix(line, "/auto\t")
}

func (u *chatUI) runChatAutoCommand(line string) {
	argument := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/auto")))
	prompt := "You> "
	if u.composing {
		prompt = "Compose> "
	}
	switch argument {
	case "", "on":
		u.manualApprovals = false
	case "off":
		u.manualApprovals = true
	case "status":
	default:
		u.print("Use /auto, /auto on, /auto off, or /auto status.\n" + prompt)
		return
	}
	state := "Auto mode is on: an automatic reviewer approves or denies agent actions instead of asking you."
	if u.manualApprovals {
		state = "Auto mode is off: Jara asks you to approve agent actions."
	}
	if argument != "status" && u.busy {
		state += " The change applies from the next turn."
	}
	u.print(state + "\n" + prompt)
}

func (u *chatUI) approvalModeLabel() string {
	if u.manualApprovals {
		return "Ask"
	}
	return "Auto"
}
