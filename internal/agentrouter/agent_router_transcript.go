package agentrouter

import (
	"encoding/json"
	"fmt"
	"strings"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
)

// historyTurns rebuilds the UI's history format from the router transcript.
func (t *Thread) HistoryTurns() []map[string]any {
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
		turns = append(turns, map[string]any{"id": turn.ID, "startedAt": turn.At, "items": items})
	}
	return turns
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

func (t *Thread) seedFromHistory(turns []protocol.HistoryTurn) {
	for _, turn := range turns {
		entry := TranscriptTurn{ID: turn.ID, Agent: protocol.AgentCodex}
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
			protocol.HistoryItem
			ExitCode *int   `json:"exitCode"`
			Output   string `json:"aggregatedOutput"`
		} `json:"item"`
		Goal *protocol.ThreadGoal `json:"goal"`
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
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
		thread.Save()
		return
	case "thread/goal/cleared":
		thread.Goal = nil
		thread.Save()
		return
	case "turn/started":
		// Codex turn IDs let /rewind revert the Codex thread natively.
		if turn := thread.lastTurn(); turn != nil && turn.ID == "" {
			turn.ID = p.Turn.ID
		}
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
		thread.Save()
	case "fileChange":
		turn.Changes = append(turn.Changes, p.Item.Changes...)
	case "commandExecution":
		turn.addAction(commandSummary(p.Item.Command, p.Item.ExitCode, p.Item.Output))
	case "plan":
		turn.addAction("planned: " + textutil.FitText(p.Item.Text, 300))
	}
}

// commandSummary records a command with its exit status and last output line
// for the switch briefing.
func commandSummary(command string, exitCode *int, output string) string {
	summary := "ran `" + textutil.FitText(command, 200) + "`"
	if exitCode != nil {
		summary += fmt.Sprintf(" → exit %d", *exitCode)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		summary += ": " + textutil.FitText(last, 160)
	}
	return summary
}

func (t *TranscriptTurn) addAction(action string) {
	if len(t.Actions) < 40 {
		t.Actions = append(t.Actions, action)
	}
}
