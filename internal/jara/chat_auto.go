package jara

import (
	"fmt"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// chatAutoApproveDelay is how long an approval that the automatic reviewer
// passed to the user waits before Jara approves it, so an unattended session
// does not stay stuck. It applies only in auto mode.
const chatAutoApproveDelay = 3 * time.Minute

// approvalsReviewer is sent with every conversation request so the agent
// uses the mode the user selected. Auto mode is the default.
func (u *chatUI) approvalsReviewer() string {
	if u.manualApprovals {
		return "user"
	}
	return protocol.AutoReviewer
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
	if argument != "status" {
		u.armAutoApproval(time.Now())
	}
	u.print(state + "\n" + prompt)
}

// toggleAutoMode flips auto mode for Shift+Tab or a tap on its control. Like
// /auto, the change applies from the next turn.
func (u *chatUI) toggleAutoMode() {
	u.manualApprovals = !u.manualApprovals
	u.armAutoApproval(time.Now())
}

// armAutoApproval starts the countdown for the oldest pending approval, or
// stops it when auto mode is off or nothing is pending.
func (u *chatUI) armAutoApproval(now time.Time) {
	u.approvalDeadline = time.Time{}
	if !u.manualApprovals && len(u.approvals) > 0 {
		u.approvalDeadline = now.Add(chatAutoApproveDelay)
	}
}

// autoApproveExpired approves the oldest pending approval once its countdown
// runs out. It reports whether it answered one.
func (u *chatUI) autoApproveExpired(now time.Time) (bool, error) {
	if u.approvalDeadline.IsZero() || len(u.approvals) == 0 || now.Before(u.approvalDeadline) {
		return false, nil
	}
	u.printRole("system", fmt.Sprintf("No answer within %s in auto mode; approving automatically.\n", formatChatAutoApproveDelay(chatAutoApproveDelay)))
	_, err := u.line("/approve")
	return true, err
}

// approvalCountdown is shown on the approval row, such as "Auto-allow in 2:59".
func (u *chatUI) approvalCountdown(now time.Time) string {
	if u.approvalDeadline.IsZero() || len(u.approvals) == 0 {
		return ""
	}
	remaining := max(0, int(u.approvalDeadline.Sub(now).Round(time.Second)/time.Second))
	return fmt.Sprintf("Auto-allow in %d:%02d", remaining/60, remaining%60)
}

func formatChatAutoApproveDelay(d time.Duration) string {
	if d%time.Minute == 0 {
		minutes := int(d / time.Minute)
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	return d.String()
}

// chatAutoModeLabel is the auto-mode control shown under the input box.
func chatAutoModeLabel(off bool) string {
	if off {
		return "◇ Auto mode off"
	}
	return "⏵⏵ Auto mode on"
}

func (u *chatUI) approvalModeLabel() string {
	if u.manualApprovals {
		return "Ask"
	}
	return "Auto"
}
