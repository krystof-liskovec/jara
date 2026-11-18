package jara

import (
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
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
	// The history replays the conversation's edits; start from none so a
	// reconnect or another conversation does not repeat or mix them.
	u.diff.resetAgentChanges()
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
				u.labelHistoryReply(item)
				u.printRoleAt("assistant", item.Text+"\n", at)
			case "plan":
				u.printRoleAt("assistant", "Plan:\n"+item.Text+"\n", at)
			case "fileChange":
				u.startFileChangeCall(item, "", at)
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
	u.labelReplies()
}

// labelHistoryReply labels a replayed reply with the model and reasoning
// that wrote it, or with its agent when those were not recorded.
func (u *chatUI) labelHistoryReply(item protocol.HistoryItem) {
	writer, ok := u.out.(interface{ SetChatAgent(string) })
	if !ok {
		return
	}
	label := u.replyLabel(item.Model, item.ResolvedModel, item.Effort)
	if label == "" {
		agent := item.Agent
		if agent == "" {
			agent = protocol.AgentForModel(u.effectiveModel)
		}
		label = protocol.AgentDisplayName(agent)
	}
	writer.SetChatAgent(label)
}

// labelReplies labels the replies that follow with the active model and
// reasoning.
func (u *chatUI) labelReplies() {
	writer, ok := u.out.(interface{ SetChatAgent(string) })
	if !ok {
		return
	}
	label := u.replyLabel(u.effectiveModel, u.resolvedModel, u.effectiveReasoning)
	if label == "" {
		label = protocol.AgentDisplayName(protocol.AgentForModel(u.effectiveModel))
	}
	writer.SetChatAgent(label)
}

// replyLabel names a model with its reasoning, such as "Claude Opus 5.5
// Extra high", or returns "" when the model is unknown.
func (u *chatUI) replyLabel(model, resolved, effort string) string {
	name := ""
	switch {
	case resolved != "" && (model == "" || protocol.AgentForModel(model) == protocol.AgentClaude):
		name = claudeModelPrettyName(chatText(resolved))
	case model != "":
		name = u.modelName(chatText(model))
	}
	if name == "" {
		return ""
	}
	if effort = strings.TrimSpace(chatText(effort)); effort != "" {
		name += " " + chatReasoningEffortLabel(effort)
	}
	return name
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
