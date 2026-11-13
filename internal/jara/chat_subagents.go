package jara

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
	"github.com/rivo/uniseg"
)

func (u *chatUI) renderLiveSubagentItem(item protocol.HistoryItem) {
	if item.Type == "subAgentActivity" && item.AgentThreadID != "" && item.AgentPath != "" {
		if u.subagentPaths == nil {
			u.subagentPaths = make(map[string]string)
		}
		u.subagentPaths[item.AgentThreadID] = item.AgentPath
	}
	u.noteSubagentItem(item, chatHistoryItemTime(item, time.Now()))
	text := chatSubagentItemText(item, u.subagentPaths)
	if text == "" {
		return
	}
	if item.ID != "" {
		if u.subagentItemDisplays == nil {
			u.subagentItemDisplays = make(map[string]string)
		}
		key := item.Type + ":" + item.ID
		if u.subagentItemDisplays[key] == text {
			return
		}
		u.subagentItemDisplays[key] = text
	}
	u.printRoleAt("subagent", text+"\n", chatHistoryItemTime(item, time.Now()))
}

func chatSubagentItemText(item protocol.HistoryItem, paths map[string]string) string {
	switch item.Type {
	case "subAgentActivity":
		verb := map[string]string{
			"started":     "Started subagent",
			"interacted":  "Interacted with subagent",
			"interrupted": "Interrupted subagent",
			"completed":   "Completed subagent",
		}[item.Kind]
		if verb == "" {
			return ""
		}
		name := chatSubagentName(item.AgentThreadID, item.AgentPath, paths)
		return verb + " · " + name
	case "collabAgentToolCall":
		return chatCollabToolSummary(item, paths)
	default:
		return ""
	}
}

func chatCollabToolSummary(item protocol.HistoryItem, paths map[string]string) string {
	action := map[string]string{
		"spawnAgent":      "Spawned subagent",
		"spawn_agent":     "Spawned subagent",
		"sendInput":       "Sent input to subagent",
		"send_input":      "Sent input to subagent",
		"resumeAgent":     "Resumed subagent",
		"resume_agent":    "Resumed subagent",
		"wait":            "Waited for subagent",
		"closeAgent":      "Closed subagent",
		"close_agent":     "Closed subagent",
		"sendMessage":     "Messaged subagent",
		"send_message":    "Messaged subagent",
		"followupTask":    "Sent follow-up to subagent",
		"followup_task":   "Sent follow-up to subagent",
		"interruptAgent":  "Interrupted subagent",
		"interrupt_agent": "Interrupted subagent",
		"listAgents":      "Listed subagents",
		"list_agents":     "Listed subagents",
	}[item.Tool]
	if action == "" {
		return ""
	}
	if (item.Tool == "spawn_agent" || item.Tool == "spawnAgent") &&
		(item.Status == "in_progress" || item.Status == "inProgress") {
		action = "Starting subagent"
	}
	line := action
	if target := chatSubagentTargets(item.ReceiverThreadIDs, paths); target != "" {
		line += " · " + target
	}
	if status := chatSubagentStatus(item.Status); status != "" {
		line += " · " + status
	}
	lines := []string{line}
	if prompt := chatSubagentPreview(item.Prompt, 240); prompt != "" {
		lines = append(lines, "Task: "+prompt)
	}
	threadIDs := make([]string, 0, len(item.AgentsStates))
	for threadID := range item.AgentsStates {
		threadIDs = append(threadIDs, threadID)
	}
	sort.Strings(threadIDs)
	for _, threadID := range threadIDs {
		state := item.AgentsStates[threadID]
		stateText := chatSubagentStatus(state.Status)
		if stateText == "" {
			stateText = "updated"
		}
		stateLine := "Agent " + chatSubagentName(threadID, "", paths) + " · " + stateText
		if message := chatSubagentPreview(state.Message, 240); message != "" {
			stateLine += ": " + message
		}
		lines = append(lines, stateLine)
	}
	return strings.Join(lines, "\n")
}

func chatSubagentTargets(threadIDs []string, paths map[string]string) string {
	if len(threadIDs) == 0 {
		return ""
	}
	names := make([]string, 0, len(threadIDs))
	for _, id := range threadIDs {
		names = append(names, chatSubagentName(id, "", paths))
	}
	sort.Strings(names)
	if len(names) > 3 {
		return fmt.Sprintf("%s and %d more", strings.Join(names[:3], ", "), len(names)-3)
	}
	return strings.Join(names, ", ")
}

func chatSubagentName(threadID, path string, paths map[string]string) string {
	if path == "" {
		path = paths[threadID]
	}
	if path != "" {
		return path
	}
	if len(threadID) > 8 {
		threadID = threadID[:8]
	}
	if threadID == "" {
		return "agent"
	}
	return "agent " + threadID
}

func chatSubagentStatus(status string) string {
	return map[string]string{
		"inProgress":   "in progress",
		"in_progress":  "in progress",
		"completed":    "completed",
		"failed":       "failed",
		"interrupted":  "interrupted",
		"pendingInit":  "starting",
		"pending_init": "starting",
		"running":      "running",
		"errored":      "failed",
		"shutdown":     "stopped",
		"notFound":     "not found",
		"not_found":    "not found",
	}[status]
}

func chatSubagentPreview(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" || limit <= 0 {
		return ""
	}
	var preview strings.Builder
	graphemes := uniseg.NewGraphemes(text)
	count := 0
	for graphemes.Next() {
		if count == limit {
			return preview.String() + "…"
		}
		preview.WriteString(graphemes.Str())
		count++
	}
	return preview.String()
}
