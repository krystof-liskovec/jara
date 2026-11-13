package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

func (u *chatUI) event(m appMessage) error {
	if m.Method == "" {
		return u.response(m)
	}
	if m.Method == "thread/settings/updated" {
		return u.acceptChatPlanUpdate(m.Params)
	}
	if m.Method == "error" {
		return u.acceptChatErrorEvent(m.Params)
	}
	var p struct {
		Delta    string
		Message  string
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID, Status string
			Error      *struct{ Message string }
		}
		Item         json.RawMessage
		ItemID       string `json:"itemId"`
		SummaryIndex int    `json:"summaryIndex"`
		Label        string `json:"label"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return err
	}
	if len(m.ID) > 0 {
		switch m.Method {
		case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
			u.approvals = append(u.approvals, m)
			u.alertUser("Approval needed")
			u.setActivityLabel("Waiting for approval")
			u.setAgentState("waiting")
			u.print(fmt.Sprintf("\nApproval required (%d pending)\n%sPress y to allow once or n to deny (or /approve, /deny).\n", len(u.approvals), formatChatApproval(m, u.items[p.ItemID])))
			return nil
		case "item/tool/requestUserInput":
			u.alertUser("Question waiting for an answer")
			return u.handleQuestionRequest(m)
		default:
			u.print("\nUnsupported interaction: " + m.Method + "\n")
			return u.c.unsupported(m)
		}
	}
	switch m.Method {
	case "thread/goal/updated":
		return u.acceptChatGoalUpdate(m.Params)
	case "thread/goal/cleared":
		return u.acceptChatGoalClear(m.Params)
	case "thread/tokenUsage/updated":
		u.acceptChatTokenUsage(m.Params)
	case "thread/compacted":
		if p.ThreadID == "" || p.ThreadID == u.thread {
			u.printRole("progress", "Conversation context compacted.\n")
		}
	case "jara/session/ended":
		var ended struct {
			Reconnect bool `json:"reconnect"`
		}
		_ = json.Unmarshal(m.Params, &ended)
		u.sessionEnded = !ended.Reconnect
	case "jara/model/resolved":
		var resolved struct {
			Model string `json:"model"`
		}
		if (p.ThreadID == "" || p.ThreadID == u.thread) && json.Unmarshal(m.Params, &resolved) == nil {
			u.resolvedModel = chatText(resolved.Model)
		}
	case "jara/activity":
		if u.busy && (p.ThreadID == "" || p.ThreadID == u.thread) && p.Label != "" {
			u.setActivityLabel(chatText(p.Label))
		}
	case "item/reasoning/summaryTextDelta":
		u.streamReasoningSummary(p.ItemID, p.SummaryIndex, p.Delta)
	case "turn/started":
		u.notePromptTurn(p.Turn.ID)
		u.attachedTurnActive = true
		u.busy = true
		u.turn = p.Turn.ID
		u.turnStartedAt = time.Now()
		if u.activityLabel == "" || u.activityLabel == "Starting turn" {
			u.setActivityLabel("Thinking")
		}
		u.setAgentState(u.currentState())
		if u.cancel {
			return u.interrupt()
		}
	case "item/agentMessage/delta", "item/commandExecution/outputDelta":
		role := "assistant"
		if m.Method == "item/commandExecution/outputDelta" {
			role = "tool"
		}
		u.printRole(role, p.Delta)
	case "item/started", "item/completed":
		var item protocol.HistoryItem
		_ = json.Unmarshal(p.Item, &item)
		u.items[item.ID] = p.Item
		u.handleChatPlanItem(m.Method, item)
		if item.Type == "contextCompaction" {
			if m.Method == "item/started" {
				u.setActivityLabel("Compacting context")
				u.printRoleAt("progress", "Compacting conversation context…\n", chatHistoryItemTime(item, time.Now()))
			} else {
				u.setActivityLabel("Thinking")
				u.contextUsage.used = 0 // unknown until the next model call
				u.printRoleAt("progress", "Conversation context compaction complete.\n", chatHistoryItemTime(item, time.Now()))
			}
		}
		if item.Type == "subAgentActivity" || item.Type == "collabAgentToolCall" {
			u.renderLiveSubagentItem(item)
		}
		if item.Type == "reasoning" && m.Method == "item/completed" {
			u.finishReasoningSummary(item.ID, item.Summary)
		}
		if m.Method == "item/started" && item.Type == "commandExecution" {
			u.setActivityLabel("Running command")
			at := time.Time{}
			if item.StartedAtMs != nil {
				at = time.UnixMilli(*item.StartedAtMs)
			}
			u.printRoleAt("tool", "\n$ "+item.Command+"\n", at)
		}
		if item.Type == "fileChange" {
			if m.Method == "item/started" {
				u.setActivityLabel("Applying changes")
			} else {
				u.setActivityLabel("Thinking")
				u.printRole("tool", "\n"+formatChatFileChanges(p.Item))
				u.diff.recordFileChanges(p.Item, true)
			}
		}
		if item.Type == "commandExecution" && m.Method == "item/completed" {
			u.setActivityLabel("Thinking")
		}
	case "turn/completed":
		u.alertTurnEnded(p.Turn.Status)
		u.finishAllReasoningSummaries()
		if err := u.cancelQuestionRequests("Turn ended before the question was answered."); err != nil {
			return err
		}
		u.attachedTurnActive = false
		u.busy = false
		u.turn = ""
		u.turnStartedAt = time.Time{}
		u.setActivityLabel("")
		u.cancel = false
		u.approvals = nil
		u.items = map[string]json.RawMessage{}
		u.subagentItemDisplays = nil
		u.setAgentState("idle")
		if !chatUIIsInteractive(u) || p.Turn.Status != "completed" {
			u.print("\n[" + p.Turn.Status + "]\n")
		}
		if p.Turn.Error != nil {
			u.print(p.Turn.Error.Message + "\n")
		}
		if retried, err := u.retryOnFallback(m.Params); retried || err != nil {
			return err
		}
		started, err := u.dispatchQueuedPrompt()
		if err != nil {
			return err
		}
		if !started {
			u.print("\nYou> ")
		}
	}
	return nil
}

func (u *chatUI) acceptChatErrorEvent(params json.RawMessage) error {
	var notification struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message   string `json:"message"`
		WillRetry bool   `json:"willRetry"`
	}
	if err := json.Unmarshal(params, &notification); err != nil || !notification.WillRetry {
		u.print("\nCodex: " + string(params) + "\n")
		return nil
	}

	message := chatText(notification.Error.Message)
	if message == "" {
		message = chatText(notification.Message)
	}
	if message == "" {
		message = "Codex is retrying after a temporary network issue."
	}
	activity := "Retrying"
	lower := strings.ToLower(message)
	if strings.Contains(lower, "network") || strings.Contains(lower, "connect") {
		activity = "Reconnecting"
		message = "Network issue: " + message
	}
	u.setActivityLabel(activity)
	u.setAgentState("running")
	u.printRole("progress", message+"\n")
	return nil
}
