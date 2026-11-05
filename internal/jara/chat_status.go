package jara

import (
	"bytes"
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
	fmt.Fprintf(&b, "Pending approvals: %d\nPending questions: %d\nPending requests: %d\n", len(u.approvals), u.pendingQuestionCount(), len(u.pending))
	for i, approval := range u.approvals {
		itemID, _ := chatApprovalSummary(approval)
		fmt.Fprintf(&b, "Pending approval %d:\n%s", i+1, formatChatApproval(approval, u.items[itemID]))
	}
	return b.String()
}

func (u *chatUI) help() string {
	return "Commands\n/new             Start a new conversation\n/sessions [query] Browse saved sessions in this folder\n/resume [N or ID]  Browse recent sessions or resume an ID\n/models          Browse and select available Codex models\n/model [ID or N] Browse models or select one directly\n/reasoning [EFFORT] Select reasoning or browse supported efforts\n/compose         Write a multi-line message\n/send            Send the composed message\n/discard         Discard the composed message\n/status          Show connection, runtime, and turn state\n/answer N X      Answer question N with option X or text\n/cancel          Stop the current turn\n/approve         Allow the oldest pending request\n/deny            Reject the oldest pending request\n/detach          Disconnect and leave the remote session running\n/quit            Stop idle sessions; leave active agents running\n/stop            Stop the remote session and release the workspace\n/help            Show available commands\n\nF2 opens model selection; F3 opens reasoning selection. Choices apply to this and future turns in this conversation; they do not change Codex defaults.\nType a message to send it to the agent. In /compose, prefix a literal slash line with //.\n"
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

// Approval requests can include safety-relevant scope and diffs. Keep both
// the request and referenced item visible, with JSON indentation for phones.
func formatChatApproval(m appMessage, item json.RawMessage) string {
	kind := "File change"
	if m.Method == "item/commandExecution/requestApproval" {
		kind = "Command"
	}
	var b strings.Builder
	b.WriteString(kind + " approval\n")
	itemID, command := chatApprovalSummary(m)
	if command == "" {
		command = chatItemCommand(item)
	}
	if command != "" {
		b.WriteString("Command: " + command + "\n")
	}
	if len(m.Params) > 0 {
		b.WriteString("Request scope:\n" + prettyChatJSON(m.Params) + "\n")
	}
	if len(item) > 0 {
		b.WriteString("Requested item and changes:\n" + prettyChatJSON(item) + "\n")
	} else if itemID != "" {
		b.WriteString("Requested item: " + itemID + " (details not received)\n")
	}
	return b.String()
}

func prettyChatJSON(raw json.RawMessage) string {
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err == nil {
		return indented.String()
	}
	return string(raw)
}
