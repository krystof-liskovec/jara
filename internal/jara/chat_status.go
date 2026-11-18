package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (u *chatUI) currentState() string {
	for _, request := range u.pending {
		switch request.method {
		case "initialize", "thread/start":
			return "starting"
		case "thread/resume":
			return "resuming"
		}
	}
	if len(u.approvals) > 0 || len(u.questionRequests) > 0 {
		return "waiting"
	}
	if u.busy {
		return "running"
	}
	return "idle"
}

func (u *chatUI) status() string {
	connection := "connecting"
	if u.connected {
		connection = "connected"
	}
	state := u.currentState()
	thread := u.thread
	if thread == "" {
		thread = "not open"
	}
	runtime := "unknown"
	if !u.startedAt.IsZero() {
		runtime = time.Since(u.startedAt).Round(time.Second).String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Status\nConnection: %s\nSession runtime: %s\nConversation: %s\nState: %s\n", connection, runtime, thread, state)
	fmt.Fprintf(&b, "Model: %s\n", u.effectiveModelLabel())
	mode := "Default"
	if u.planMode {
		mode = "Plan"
	}
	fmt.Fprintf(&b, "Mode: %s\n", mode)
	fmt.Fprintf(&b, "Approvals: %s\n", u.approvalModeLabel())
	if u.nextModel != "" {
		fmt.Fprintf(&b, "Next turns model: %s (selected)\n", u.nextModelLabel)
	}
	currentReasoning := u.effectiveReasoning
	if currentReasoning == "" {
		currentReasoning = "model default"
	}
	fmt.Fprintf(&b, "Reasoning: %s\n", chatReasoningEffortLabel(currentReasoning))
	if u.nextReasoning != "" {
		fmt.Fprintf(&b, "Next turns reasoning: %s (selected)\n", chatReasoningEffortLabel(u.nextReasoning))
	}
	b.WriteString(u.contextStatusLine())
	if u.turn != "" {
		elapsed := "unknown"
		if !u.turnStartedAt.IsZero() {
			elapsed = time.Since(u.turnStartedAt).Round(time.Second).String()
		}
		fmt.Fprintf(&b, "Turn: %s (%s)\n", u.turn, elapsed)
	} else if u.busy && state == "running" {
		b.WriteString("Turn: starting\n")
	} else {
		b.WriteString("Turn: idle\n")
	}
	fmt.Fprintf(&b, "Pending approvals: %d\nPending questions: %d\nPending requests: %d\nQueued prompts: %d\n", len(u.approvals), u.pendingQuestionCount(), len(u.pending), len(u.queuedPrompts))
	for i, approval := range u.approvals {
		itemID, _ := chatApprovalSummary(approval)
		fmt.Fprintf(&b, "Pending approval %d:\n%s", i+1, formatChatApproval(approval, u.items[itemID]))
	}
	return b.String()
}

func chatApprovalSummary(m appMessage) (itemID, command string) {
	var request struct {
		ItemID  string `json:"itemId"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(m.Params, &request)
	return request.ItemID, request.Command
}

func chatItemCommand(raw json.RawMessage) string {
	var item struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(raw, &item)
	return item.Command
}
