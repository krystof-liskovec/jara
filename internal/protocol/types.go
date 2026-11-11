package protocol

import (
	"encoding/json"
	"unicode"
)

type CustomCommand struct {
	Name         string `json:"name"` // without the slash; folders join with ':'
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	Body         string `json:"body"`
	Source       string `json:"source"` // codex, claude, or project
}

func ValidCustomCommandName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ':' || r == '.') {
			return false
		}
	}
	return true
}

type DefaultModels struct {
	Agent    string `json:"agent"`              // agent new conversations start with
	Codex    string `json:"codex"`              // Codex model ID, empty when unknown
	Claude   string `json:"claude"`             // Claude UI model ID, e.g. claude/opus
	Default  string `json:"default,omitempty"`  // host default across providers
	Fallback string `json:"fallback,omitempty"` // model used past a usage limit
}

// AutoReviewer is the app-server approvalsReviewer for auto mode. Codex
// routes approval requests to its auto-review subagent; Jara's agent router
// runs Claude Code in its auto permission mode.
const AutoReviewer = "auto_review"

func (d *DefaultModels) ForAgent(agent string) string {
	if d == nil {
		return ""
	}
	if agent == AgentClaude {
		return d.Claude
	}
	return d.Codex
}

type FileChange struct {
	Path    string          `json:"path"`
	OldPath string          `json:"oldPath"`
	NewPath string          `json:"newPath"`
	Kind    json.RawMessage `json:"kind"`
	Diff    string          `json:"diff"`
}

type ThreadGoal struct {
	ThreadID        string `json:"threadId"`
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokenBudget     *int64 `json:"tokenBudget"`
	TokensUsed      int64  `json:"tokensUsed"`
	TimeUsedSeconds int64  `json:"timeUsedSeconds"`
}

type HistoryTurn struct {
	ID        string        `json:"id"`
	StartedAt *int64        `json:"startedAt"`
	Items     []HistoryItem `json:"items"`
}

type HistoryContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type SubagentAgentState struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type HistoryItem struct {
	ID                string                        `json:"id"`
	Type              string                        `json:"type"`
	Text              string                        `json:"text"`
	Command           string                        `json:"command"`
	Status            string                        `json:"status"`
	Changes           []FileChange                  `json:"changes"`
	Summary           []ReasoningSummaryPart        `json:"summary"`
	Content           []HistoryContent              `json:"content"`
	StartedAtMs       *int64                        `json:"startedAtMs"`
	CompletedAtMs     *int64                        `json:"completedAtMs"`
	Kind              string                        `json:"kind"`
	AgentThreadID     string                        `json:"agentThreadId"`
	AgentPath         string                        `json:"agentPath"`
	Tool              string                        `json:"tool"`
	Prompt            string                        `json:"prompt"`
	ReceiverThreadIDs []string                      `json:"receiverThreadIds"`
	AgentsStates      map[string]SubagentAgentState `json:"agentsStates"`
	Agent             string                        `json:"jaraAgent"` // set by the agent router
}

const ModelPageSize = 100

type CodexModel struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"displayName"`
	Hidden                    bool                    `json:"hidden"`
	IsDefault                 bool                    `json:"isDefault"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
}

type ReasoningEffortOption struct {
	Value       string `json:"reasoningEffort"`
	Description string `json:"description"`
}

func ValidModelID(value string) bool {
	if value == "" || len(value) > ModelMaxIDBytes {
		return false
	}
	for _, r := range value {
		if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

type ReasoningSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (p *ReasoningSummaryPart) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		p.Type = "summary_text"
		p.Text = text
		return nil
	}
	type summaryPart ReasoningSummaryPart
	var decoded summaryPart
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = ReasoningSummaryPart(decoded)
	return nil
}
