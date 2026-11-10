package jara

import (
	"encoding/json"
	"fmt"
	"strings"
)

// historyTurns rebuilds the UI's history format from the router transcript.
func (t *routedThread) historyTurns() []map[string]any {
	turns := make([]map[string]any, 0, len(t.Turns))
	for i, turn := range t.Turns {
		at := turn.At * 1000
		items := []map[string]any{{"id": fmt.Sprintf("u%d", i), "type": "userMessage", "content": []map[string]string{{"type": "text", "text": turn.User}}}}
		if len(turn.Changes) > 0 {
			items = append(items, map[string]any{"id": fmt.Sprintf("f%d", i), "type": "fileChange", "status": "completed", "changes": turn.Changes})
		}
		if turn.Reply != "" {
			items = append(items, map[string]any{"id": fmt.Sprintf("a%d", i), "type": "agentMessage", "text": turn.Reply, "startedAtMs": at, "jaraAgent": turn.Agent})
		}
		turns = append(turns, map[string]any{"startedAt": turn.At, "items": items})
	}
	return turns
}

// switchBriefing tells the receiving agent what happened before it joined.
// Agent histories are not interchangeable, so the earlier exchange is replayed
// as text; the working tree already contains the file changes.
func (t *routedThread) switchBriefing(from, to, message string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Jara: this conversation started with %s. The user switched it to you (%s). The earlier exchange is replayed below; "+
		"files in the working tree already include every change made so far. Continue from here.]\n\n", agentDisplayName(from), agentDisplayName(to))
	var replay []string
	for _, turn := range t.Turns {
		entry := "User: " + turn.User
		if turn.Reply != "" {
			entry += "\n" + agentDisplayName(turn.Agent) + ": " + turn.Reply
		}
		for _, action := range turn.Actions {
			entry += "\n(" + agentDisplayName(turn.Agent) + " " + action + ")"
		}
		for _, change := range turn.Changes {
			entry += "\n(" + agentDisplayName(turn.Agent) + " edited " + firstNonEmpty(change.Path, change.NewPath, change.OldPath) + ")"
		}
		replay = append(replay, entry)
	}
	if t.Goal != nil && t.Goal.Status == "active" {
		fmt.Fprintf(&b, "[Active goal: %s]\n\n", t.Goal.Objective)
	}
	text := strings.Join(replay, "\n\n")
	if len(text) > 40000 {
		text = "… (earlier turns omitted)\n" + text[len(text)-40000:]
	}
	b.WriteString(text)
	b.WriteString("\n\n[New message from the user]\n" + message)
	return b.String()
}

func turnText(params json.RawMessage) string {
	var request struct {
		Input []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
	}
	_ = json.Unmarshal(params, &request)
	var parts []string
	for _, input := range request.Input {
		if input.Type == "text" {
			parts = append(parts, input.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (t *routedThread) seedFromHistory(turns []chatHistoryTurn) {
	for _, turn := range turns {
		entry := transcriptTurn{Agent: agentCodex}
		if turn.StartedAt != nil {
			entry.At = *turn.StartedAt
		}
		for _, item := range turn.Items {
			switch item.Type {
			case "userMessage":
				for _, content := range item.Content {
					entry.User += content.Text
				}
			case "agentMessage":
				entry.Reply = item.Text
			case "fileChange":
				entry.Changes = append(entry.Changes, item.Changes...)
			}
		}
		t.Turns = append(t.Turns, entry)
	}
}

// observeCodexNotification keeps the transcript of Codex turns so a later
// switch to Claude Code can brief it.
func (r *agentRouter) observeCodexNotification(m rpcMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Item     struct {
			chatHistoryItem
			ExitCode *int   `json:"exitCode"`
			Output   string `json:"aggregatedOutput"`
		} `json:"item"`
		Goal *chatThreadGoal `json:"goal"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	thread := r.threads[p.ThreadID]
	if thread == nil {
		return
	}
	switch m.Method {
	case "thread/goal/updated":
		thread.Goal = p.Goal // kept so a switch to Claude Code carries the goal
		thread.save()
		return
	case "thread/goal/cleared":
		thread.Goal = nil
		thread.save()
		return
	case "item/completed":
	default:
		return
	}
	turn := thread.lastTurn()
	if turn == nil {
		return
	}
	switch p.Item.Type {
	case "agentMessage":
		turn.Reply = p.Item.Text
		thread.save()
	case "fileChange":
		turn.Changes = append(turn.Changes, p.Item.Changes...)
	case "commandExecution":
		turn.addAction(commandSummary(p.Item.Command, p.Item.ExitCode, p.Item.Output))
	case "plan":
		turn.addAction("planned: " + fitMenu(p.Item.Text, 300))
	}
}

// commandSummary records a command with its exit status and last output line
// for the switch briefing.
func commandSummary(command string, exitCode *int, output string) string {
	summary := "ran `" + fitMenu(command, 200) + "`"
	if exitCode != nil {
		summary += fmt.Sprintf(" → exit %d", *exitCode)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		summary += ": " + fitMenu(last, 160)
	}
	return summary
}

func (t *transcriptTurn) addAction(action string) {
	if len(t.Actions) < 40 {
		t.Actions = append(t.Actions, action)
	}
}
