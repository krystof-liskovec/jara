package protocol

import (
	"encoding/json"
	"strings"
)

// Claude Code models appear in the conversation UI's model list with a
// "claude/" prefix, which also tells the router which agent serves them.
const ClaudeModelPrefix = "claude/"
const ClaudeDefaultModel = "claude/default"

var claudeModelAliases = []struct{ alias, name string }{
	{"default", "Claude Code (default model)"},
	{"opus", "Claude Opus"},
	{"sonnet", "Claude Sonnet"},
	{"haiku", "Claude Haiku"},
	{"fable", "Claude Fable"},
}

var ClaudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// ClaudeCLIModel converts a UI model ID to Claude Code's --model value; the
// default entry leaves Claude Code's own configuration in charge.
func ClaudeCLIModel(model string) string {
	alias := strings.TrimPrefix(model, ClaudeModelPrefix)
	if alias == "default" || alias == "" || alias == model {
		return ""
	}
	return alias
}

func ClaudeModelEntries() []CodexModel {
	if !ClaudeAvailable() {
		return nil
	}
	options := make([]ReasoningEffortOption, 0, len(ClaudeEfforts))
	for _, effort := range ClaudeEfforts {
		options = append(options, ReasoningEffortOption{Value: effort, Description: "Claude Code --effort " + effort})
	}
	entries := make([]CodexModel, 0, len(claudeModelAliases))
	for _, alias := range claudeModelAliases {
		id := ClaudeModelPrefix + alias.alias
		entries = append(entries, CodexModel{ID: id, Model: id, DisplayName: alias.name,
			DefaultReasoningEffort: "medium", SupportedReasoningEfforts: options})
	}
	return entries
}

// MergeClaudeModels appends Claude entries to the final page of Codex's model
// list so the picker offers both agents.
func MergeClaudeModels(result json.RawMessage) json.RawMessage {
	var page map[string]json.RawMessage
	if json.Unmarshal(result, &page) != nil {
		return result
	}
	var cursor *string
	_ = json.Unmarshal(page["nextCursor"], &cursor)
	if cursor != nil && *cursor != "" {
		return result
	}
	var data []json.RawMessage
	_ = json.Unmarshal(page["data"], &data)
	for _, entry := range ClaudeModelEntries() {
		encoded, _ := json.Marshal(entry)
		data = append(data, encoded)
	}
	page["data"], _ = json.Marshal(data)
	out, err := json.Marshal(page)
	if err != nil {
		return result
	}
	return out
}

// ClaudeModelDisplayName names Claude entries before the model list loads;
// other model IDs are returned unchanged.
func ClaudeModelDisplayName(model string) string {
	for _, alias := range claudeModelAliases {
		if model == ClaudeModelPrefix+alias.alias {
			return alias.name
		}
	}
	return model
}
