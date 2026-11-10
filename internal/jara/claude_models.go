package jara

import (
	"encoding/json"
	"strings"
)

// Claude Code models appear in the conversation UI's model list with a
// "claude/" prefix, which also tells the router which agent serves them.
const claudeModelPrefix = "claude/"
const claudeDefaultModel = "claude/default"

var claudeModelAliases = []struct{ alias, name string }{
	{"default", "Claude Code (default model)"},
	{"opus", "Claude Opus"},
	{"sonnet", "Claude Sonnet"},
	{"haiku", "Claude Haiku"},
	{"fable", "Claude Fable"},
}

var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

func agentForModel(model string) string {
	if strings.HasPrefix(model, claudeModelPrefix) {
		return agentClaude
	}
	return agentCodex
}

// claudeCLIModel converts a UI model ID to Claude Code's --model value; the
// default entry leaves Claude Code's own configuration in charge.
func claudeCLIModel(model string) string {
	alias := strings.TrimPrefix(model, claudeModelPrefix)
	if alias == "default" || alias == "" || alias == model {
		return ""
	}
	return alias
}

func claudeModelEntries() []codexModel {
	if !claudeAvailable() {
		return nil
	}
	options := make([]chatReasoningEffortOption, 0, len(claudeEfforts))
	for _, effort := range claudeEfforts {
		options = append(options, chatReasoningEffortOption{Value: effort, Description: "Claude Code --effort " + effort})
	}
	entries := make([]codexModel, 0, len(claudeModelAliases))
	for _, alias := range claudeModelAliases {
		id := claudeModelPrefix + alias.alias
		entries = append(entries, codexModel{ID: id, Model: id, DisplayName: alias.name,
			DefaultReasoningEffort: "medium", SupportedReasoningEfforts: options})
	}
	return entries
}

// mergeClaudeModels appends Claude entries to the final page of Codex's model
// list so the picker offers both agents.
func mergeClaudeModels(result json.RawMessage) json.RawMessage {
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
	for _, entry := range claudeModelEntries() {
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

// claudeModelDisplayName names Claude entries before the model list loads;
// other model IDs are returned unchanged.
func claudeModelDisplayName(model string) string {
	for _, alias := range claudeModelAliases {
		if model == claudeModelPrefix+alias.alias {
			return alias.name
		}
	}
	return model
}
