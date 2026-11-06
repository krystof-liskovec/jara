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
	mode := "Default"
	if u.planMode {
		mode = "Plan"
	}
	fmt.Fprintf(&b, "Mode: %s\n", mode)
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
	fmt.Fprintf(&b, "Pending approvals: %d\nPending questions: %d\nPending requests: %d\nQueued prompts: %d\n", len(u.approvals), u.pendingQuestionCount(), len(u.pending), len(u.queuedPrompts))
	for i, approval := range u.approvals {
		itemID, _ := chatApprovalSummary(approval)
		fmt.Fprintf(&b, "Pending approval %d:\n%s", i+1, formatChatApproval(approval, u.items[itemID]))
	}
	return b.String()
}

func (u *chatUI) help() string {
	return `Commands
/new             Start a new conversation
/sessions [query] Browse saved sessions in this folder
/resume [N or ID]  Browse recent sessions or resume an ID
/models          Browse and select available Codex models
/model [ID or N] Browse models or select one directly
/reasoning [EFFORT] Select reasoning or browse supported efforts
/compose         Write a multi-line message
/send            Send the composed message
/discard         Discard the composed message
/goal [ARGS]     Set or manage a persistent goal for this conversation
/plan [PROMPT|off|status] Use Plan mode for this and later turns
/goalcrunch ...  Run a goal through usage-limit pauses
/schedule ...    Schedule a prompt for this conversation
/crunch ...      Run a prompt through usage-limit pauses
/queue [run|clear|edit N] Inspect or manage queued prompts
/status          Show connection, runtime, and turn state
/answer N X      Answer question N with option X or text
/cancel          Stop the current turn
/approve         Allow the oldest pending request
/deny            Reject the oldest pending request
/detach          Disconnect and leave the remote session running
/quit            Stop idle sessions; leave active agents running
/stop            Stop the remote session and release the workspace
/help            Show available commands
!COMMAND         Run a shell command in this workspace

Use /crunch PROMPT to work until the task is complete. Normal completion stops it; a usage-limit interruption waits for reset and resumes. /goalcrunch OBJECTIVE stops when Codex marks its goal complete. Both require a persistent Jara session. Use /crunch status or /crunch stop to manage the active crunch task.
Use /schedule in 30m PROMPT, /schedule at RFC3339 PROMPT, /schedule list, or /schedule cancel ID. Scheduled prompts run when this conversation is idle and continue after you disconnect.
While a turn is running, type a follow-up and press Tab to queue it. Queued prompts run in order after the current turn; use /queue to inspect them, /queue run to start one when idle, /queue edit N to edit one in the interactive interface, or /queue clear to remove this conversation's queued prompts.
F2 opens model selection; F3 opens reasoning selection. Choices apply to this and future turns in this conversation; they do not change Codex defaults.
Hold Shift while dragging over chat history to select text with your terminal, then use its copy shortcut.
Type a message to send it to the agent. In /compose, prefix a literal slash line with //.
`
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
