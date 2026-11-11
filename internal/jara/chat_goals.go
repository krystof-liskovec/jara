package jara

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

const chatGoalUsage = "Usage: /goal <objective> | /goal pause | /goal resume | /goal clear | /goal edit <objective>\n"

func isChatGoalCommand(line string) bool {
	return line == "/goal" || strings.HasPrefix(line, "/goal ") || strings.HasPrefix(line, "/goal\t")
}

func (u *chatUI) runChatGoalCommand(line string) error {
	if !u.threadReady || u.thread == "" {
		u.print("No conversation is open. Use /resume ID or /new first.\n")
		return nil
	}
	args := strings.TrimSpace(strings.TrimPrefix(line, "/goal"))
	if args == "" {
		return u.requestWithAction("thread/goal/get", map[string]string{"threadId": u.thread}, "")
	}
	switch args {
	case "pause":
		return u.setChatGoalStatus("paused", "pause")
	case "resume":
		return u.setChatGoalStatus("active", "resume")
	case "clear":
		return u.requestWithAction("thread/goal/clear", map[string]string{
			"threadId": u.thread,
			"origin":   "user",
		}, "clear")
	}
	if args == "edit" {
		u.print(chatGoalUsage)
		return nil
	}
	action := "set"
	if strings.HasPrefix(args, "edit ") || strings.HasPrefix(args, "edit\t") {
		args = strings.TrimSpace(args[len("edit"):])
		action = "edit"
	}
	if args == "" {
		u.print(chatGoalUsage)
		return nil
	}
	if utf8.RuneCountInString(args) > 4000 {
		u.print("Goal objectives must be 4,000 characters or fewer.\n" + chatGoalUsage)
		return nil
	}
	return u.requestWithAction("thread/goal/set", map[string]any{
		"threadId":  u.thread,
		"origin":    "user",
		"objective": args,
		"status":    "active",
	}, action)
}

func (u *chatUI) setChatGoalStatus(status, action string) error {
	return u.requestWithAction("thread/goal/set", map[string]any{
		"threadId": u.thread,
		"origin":   "user",
		"status":   status,
	}, action)
}

func chatGoalRequest(method string) bool {
	switch method {
	case "thread/goal/get", "thread/goal/set", "thread/goal/clear":
		return true
	default:
		return false
	}
}

// acceptChatGoalResponse handles the reply to a goal request. JSON-RPC
// responses carry no method, so the caller passes the request's method.
func (u *chatUI) acceptChatGoalResponse(m appMessage, method, action string) error {
	message := ""
	switch method {
	case "thread/goal/get":
		var response struct {
			Goal *protocol.ThreadGoal `json:"goal"`
		}
		if err := json.Unmarshal(m.Result, &response); err != nil {
			return err
		}
		u.goal = response.Goal
		if u.goal == nil {
			message = "No goal is set.\n" + chatGoalUsage
		} else {
			message = formatChatGoal(*u.goal)
		}
	case "thread/goal/set":
		if strings.HasPrefix(action, "goalcrunch:") {
			var response struct {
				Goal *protocol.ThreadGoal `json:"goal"`
			}
			if len(m.Result) > 0 && json.Unmarshal(m.Result, &response) == nil && response.Goal != nil {
				u.goal = response.Goal
			}
			objective := strings.TrimPrefix(action, "goalcrunch:")
			return u.requestWithAction("jara/crunch/create", map[string]any{
				"threadId": u.thread,
				"mode":     "goal",
				"prompt":   objective,
			}, "create")
		}
		var response struct {
			Goal *protocol.ThreadGoal `json:"goal"`
		}
		if len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, &response); err != nil {
				return err
			}
		}
		if response.Goal != nil {
			u.goal = response.Goal
		}
		switch action {
		case "pause":
			message = "Goal paused."
		case "resume":
			message = "Goal resumed."
		case "edit":
			message = "Goal updated."
		default:
			message = "Goal set."
		}
		if u.goal != nil {
			message += " Status: " + chatGoalStatusLabel(u.goal.Status) + "."
		}
	case "thread/goal/clear":
		var response struct {
			Cleared bool `json:"cleared"`
		}
		if len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, &response); err != nil {
				return err
			}
		}
		u.goal = nil
		if response.Cleared {
			message = "Goal cleared."
		} else {
			message = "No goal was set."
		}
	}
	u.print("\n" + message + "\n" + u.chatGoalPrompt())
	return nil
}

func (u *chatUI) acceptChatGoalUpdate(raw json.RawMessage) error {
	var notification struct {
		ThreadID string              `json:"threadId"`
		Goal     protocol.ThreadGoal `json:"goal"`
	}
	if err := json.Unmarshal(raw, &notification); err != nil {
		return err
	}
	if notification.ThreadID != "" && notification.ThreadID != u.thread {
		return nil
	}
	previous := ""
	if u.goal != nil {
		previous = u.goal.Status
	}
	u.goal = &notification.Goal
	if notification.Goal.Status != "active" && previous != notification.Goal.Status && !u.chatGoalMutationPending() {
		u.print("\nGoal is " + chatGoalStatusLabel(notification.Goal.Status) + ".\n" + u.chatGoalPrompt())
	}
	return nil
}

func (u *chatUI) acceptChatGoalClear(raw json.RawMessage) error {
	var notification struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(raw, &notification); err != nil {
		return err
	}
	if notification.ThreadID != "" && notification.ThreadID != u.thread {
		return nil
	}
	wasSet := u.goal != nil
	u.goal = nil
	if wasSet && !u.chatGoalMutationPending() {
		u.print("\nGoal cleared.\n" + u.chatGoalPrompt())
	}
	return nil
}

func (u *chatUI) chatGoalMutationPending() bool {
	for _, request := range u.pending {
		if request.threadID == u.thread && (request.method == "thread/goal/set" || request.method == "thread/goal/clear") {
			return true
		}
	}
	return false
}

func (u *chatUI) chatGoalPrompt() string {
	if u.busy {
		return ""
	}
	if u.composing {
		return "Compose> "
	}
	return "You> "
}

func chatGoalRequestError(message string) string {
	detail := strings.TrimSpace(chatText(message))
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "goals feature is disabled"):
		return "Goals are disabled in Codex. Set goals = true under [features] in config.toml and restart Codex."
	case strings.Contains(lower, "method not found"):
		return "This Codex app-server does not support Goals. Update Codex to a release with Goal support."
	case strings.Contains(lower, "ephemeral"):
		return "Goals require a saved conversation. Use /resume to open a saved conversation."
	default:
		return "Goal command failed: " + detail
	}
}

func formatChatGoal(goal protocol.ThreadGoal) string {
	var result strings.Builder
	fmt.Fprintf(&result, "Goal (%s)\nObjective: %s\nTime used: %s\n", chatGoalStatusLabel(goal.Status), goal.Objective, formatChatGoalTime(goal.TimeUsedSeconds))
	if goal.TokenBudget == nil {
		fmt.Fprintf(&result, "Tokens used: %s\n", strconv.FormatInt(goal.TokensUsed, 10))
	} else {
		fmt.Fprintf(&result, "Tokens: %s / %s\n", strconv.FormatInt(goal.TokensUsed, 10), strconv.FormatInt(*goal.TokenBudget, 10))
	}
	return result.String()
}

func formatChatGoalTime(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return strconv.FormatInt(seconds, 10) + "s"
	}
	minutes := seconds / 60
	if minutes < 60 {
		return strconv.FormatInt(minutes, 10) + "m"
	}
	hours := minutes / 60
	if hours >= 24 {
		return fmt.Sprintf("%dd %dh %dm", hours/24, hours%24, minutes%60)
	}
	if minutes%60 == 0 {
		return strconv.FormatInt(hours, 10) + "h"
	}
	return fmt.Sprintf("%dh %dm", hours, minutes%60)
}

func chatGoalStatusLabel(status string) string {
	switch status {
	case "active":
		return "active"
	case "paused":
		return "paused"
	case "blocked":
		return "stalled"
	case "usageLimited":
		return "usage limited"
	case "budgetLimited":
		return "limited by budget"
	case "complete":
		return "complete"
	default:
		return status
	}
}
