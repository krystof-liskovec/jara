package jara

import (
	"strings"
	"time"
)

type chatThreadResponse struct {
	Thread struct {
		ID              string            `json:"id"`
		ReasoningEffort *string           `json:"reasoningEffort"`
		Turns           []chatHistoryTurn `json:"turns"`
	} `json:"thread"`
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
}

type chatHistoryTurn struct {
	StartedAt *int64            `json:"startedAt"`
	Items     []chatHistoryItem `json:"items"`
}

type chatHistoryContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type chatSubagentAgentState struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type chatHistoryItem struct {
	ID                string                            `json:"id"`
	Type              string                            `json:"type"`
	Text              string                            `json:"text"`
	Command           string                            `json:"command"`
	Status            string                            `json:"status"`
	Summary           []chatReasoningSummaryPart        `json:"summary"`
	Content           []chatHistoryContent              `json:"content"`
	StartedAtMs       *int64                            `json:"startedAtMs"`
	CompletedAtMs     *int64                            `json:"completedAtMs"`
	Kind              string                            `json:"kind"`
	AgentThreadID     string                            `json:"agentThreadId"`
	AgentPath         string                            `json:"agentPath"`
	Tool              string                            `json:"tool"`
	Prompt            string                            `json:"prompt"`
	ReceiverThreadIDs []string                          `json:"receiverThreadIds"`
	AgentsStates      map[string]chatSubagentAgentState `json:"agentsStates"`
}

func (u *chatUI) renderChatHistory(turns []chatHistoryTurn) {
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
				u.printRoleAt("assistant", item.Text+"\n", at)
			case "plan":
				u.printRoleAt("assistant", "Plan:\n"+item.Text+"\n", at)
			case "reasoning":
				u.renderReasoningSummaryAt(item.Summary, at)
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
				}
			case "subAgentActivity", "collabAgentToolCall":
				if text := chatSubagentItemText(item, paths); text != "" {
					u.printRoleAt("subagent", text+"\n", at)
				}
			}
		}
	}
}

func chatHistoryItemTime(item chatHistoryItem, fallback time.Time) time.Time {
	if item.StartedAtMs != nil {
		return time.UnixMilli(*item.StartedAtMs)
	}
	if item.CompletedAtMs != nil {
		return time.UnixMilli(*item.CompletedAtMs)
	}
	return fallback
}
