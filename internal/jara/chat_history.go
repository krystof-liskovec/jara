package jara

import (
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

type chatThreadResponse struct {
	Thread struct {
		ID              string                 `json:"id"`
		ReasoningEffort *string                `json:"reasoningEffort"`
		Turns           []protocol.HistoryTurn `json:"turns"`
	} `json:"thread"`
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	ResolvedModel   string  `json:"resolvedModel"` // concrete model behind an alias
}

func (u *chatUI) renderChatHistory(turns []protocol.HistoryTurn) {
	u.promptLog = nil
	for _, turn := range turns {
		paths := make(map[string]string)
		for _, item := range turn.Items {
			if item.Type == "subAgentActivity" && item.AgentThreadID != "" && item.AgentPath != "" {
				paths[item.AgentThreadID] = item.AgentPath
			}
		}
		fallback := time.Time{}
		if turn.StartedAt != nil {
			fallback = time.Unix(*turn.StartedAt, 0)
		}
		for _, item := range turn.Items {
			at := chatHistoryItemTime(item, fallback)
			switch item.Type {
			case "agentMessage":
				u.labelHistoryAgent(item.Agent)
				u.printRoleAt("assistant", item.Text+"\n", at)
			case "plan":
				u.printRoleAt("assistant", "Plan:\n"+item.Text+"\n", at)
			case "fileChange":
				u.printRoleAt("tool", formatChatFileChangeList(item.Status, item.Changes), at)
				u.diff.record(item.Changes, false)
			case "reasoning":
				if !chatUIIsInteractive(u) {
					u.renderReasoningSummaryAt(item.Summary, at)
				}
			case "contextCompaction":
				u.printRoleAt("progress", "Conversation context was compacted.\n", at)
			case "userMessage":
				var text strings.Builder
				for _, content := range item.Content {
					if content.Type == "text" || content.Type == "input_text" {
						text.WriteString(content.Text)
					}
				}
				if text.Len() > 0 {
					u.printRoleAt("user", text.String()+"\n", at)
					u.promptLog = append(u.promptLog, chatPromptEntry{turnID: turn.ID, text: text.String()})
				}
			case "subAgentActivity", "collabAgentToolCall":
				if u.subagentPaths == nil {
					u.subagentPaths = map[string]string{}
				}
				for id, path := range paths {
					u.subagentPaths[id] = path
				}
				u.noteSubagentItem(item, at)
				if text := chatSubagentItemText(item, paths); text != "" {
					u.printRoleAt("subagent", text+"\n", at)
				}
			}
		}
	}
	u.labelHistoryAgent("")
}

// labelHistoryAgent labels replayed replies with the agent that wrote them;
// an empty agent restores the label of the active model.
func (u *chatUI) labelHistoryAgent(agent string) {
	writer, ok := u.out.(interface{ SetChatAgent(string) })
	if !ok {
		return
	}
	if agent == "" {
		agent = protocol.AgentForModel(u.effectiveModel)
	}
	writer.SetChatAgent(protocol.AgentDisplayName(agent))
}

func chatHistoryItemTime(item protocol.HistoryItem, fallback time.Time) time.Time {
	if item.StartedAtMs != nil {
		return time.UnixMilli(*item.StartedAtMs)
	}
	if item.CompletedAtMs != nil {
		return time.UnixMilli(*item.CompletedAtMs)
	}
	return fallback
}
