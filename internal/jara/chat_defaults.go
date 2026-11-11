package jara

import (
	"encoding/json"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// The model controls name the concrete model: the conversation's model once
// it opens, and the host's default for new conversations before that.

func (u *chatUI) requestDefaultModels() error {
	id, err := u.c.request("jara/defaultModel/get", map[string]string{"cwd": u.cwd})
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{method: "jara/defaultModel/get", deadline: time.Now().Add(time.Minute)}
	return nil
}

// expireChatDefaultRequest drops a default-model request that timed out;
// the conversation keeps working without it.
func (u *chatUI) expireChatDefaultRequest(id string, pending chatPending) bool {
	if !chatDefaultRequest(pending.method) {
		return false
	}
	delete(u.pending, id)
	if pending.method == "jara/defaultModel/get" {
		u.defaults = &protocol.DefaultModels{}
	} else {
		u.printRole("system", "Saving the model choice timed out.\n")
	}
	return true
}

func (u *chatUI) acceptChatDefaultResponse(m appMessage, p chatPending) {
	if p.method == "jara/defaultModel/get" {
		u.defaults = &protocol.DefaultModels{}
		if m.Error == nil {
			_ = json.Unmarshal(m.Result, u.defaults)
		}
		return
	}
	if m.Error != nil {
		u.printRole("system", "Could not save the model choice: "+chatText(m.Error.Message)+"\n")
		return
	}
	if u.defaults == nil {
		u.defaults = &protocol.DefaultModels{}
	}
	if p.method == "jara/fallbackModel/set" {
		u.defaults.Fallback = p.action
		if p.action == "" {
			u.printRole("system", "Fallback model turned off; usage limits wait for the reset.\n")
		} else {
			u.printRole("system", "Fallback model: "+u.modelName(p.action)+". When a turn or /crunch hits a usage limit, Jara continues on it.\n")
		}
		return
	}
	agent := protocol.AgentForModel(p.action)
	u.defaults.Default, u.defaults.Agent = p.action, agent
	if agent == protocol.AgentClaude {
		u.defaults.Claude = p.action
	} else {
		u.defaults.Codex = p.action
	}
	u.printRole("system", "Default model for new conversations: "+u.modelName(p.action)+" ("+protocol.AgentDisplayName(agent)+
		"). "+protocol.AgentDisplayName(agent)+" terminals on this host use it too; --agent still picks a provider explicitly.\n")
}

func chatDefaultRequest(method string) bool {
	return method == "jara/defaultModel/get" || method == "jara/defaultModel/set" || method == "jara/fallbackModel/set"
}

func isChatDefaultCommand(line string) bool {
	line = strings.TrimSpace(line)
	return line == "/default" || strings.HasPrefix(line, "/default ") || strings.HasPrefix(line, "/default\t")
}

func (u *chatUI) runChatDefaultCommand(line string) error {
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/default"))
	if value == "" {
		if chatUIIsInteractive(u) {
			u.pickerRequested = "default"
			if u.models != nil && u.models.complete && u.models.failure == "" {
				return nil
			}
			if err := u.startChatModelList(); err != nil {
				u.pickerRequested = ""
				u.print("\n" + err.Error() + "\n")
			}
			return nil
		}
		u.print("Default models: Codex " + u.defaultLabel(protocol.AgentCodex) + ", Claude Code " + u.defaultLabel(protocol.AgentClaude) +
			".\nUse /default ID or /default N to set the default model for new conversations.\n")
		return nil
	}
	if u.models == nil {
		u.models = newChatModelBrowser()
		u.models.complete = true
	}
	id, _, err := u.models.model(value)
	if err != nil {
		u.print(err.Error() + "\n")
		return nil
	}
	return u.setChatDefaultModel(id)
}

func (u *chatUI) setChatDefaultModel(model string) error {
	id, err := u.c.request("jara/defaultModel/set", map[string]string{"model": model})
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{method: "jara/defaultModel/set", deadline: time.Now().Add(time.Minute), action: model}
	return nil
}

func (u *chatUI) defaultLabel(agent string) string {
	if model := u.defaults.ForAgent(agent); model != "" {
		return u.modelName(model)
	}
	if agent == protocol.AgentClaude {
		return u.modelName(protocol.ClaudeDefaultModel) // Claude Code picks its own model
	}
	return "not reported"
}

// newConversationModelLabel names the model a new conversation would use.
func (u *chatUI) newConversationModelLabel() string {
	if u.defaults == nil {
		return "Loading…"
	}
	agent := u.defaults.Agent
	if agent == "" {
		agent = protocol.AgentCodex
	}
	if u.defaults.Default != "" && protocol.AgentForModel(u.defaults.Default) == agent {
		return u.modelName(u.defaults.Default)
	}
	return u.defaultLabel(agent)
}

// modelName names a model ID for display, preferring the catalog's name and
// otherwise Claude Code's configured or reported concrete model.
func (u *chatUI) modelName(model string) string {
	if model == protocol.ClaudeDefaultModel {
		if configured := u.defaults.ForAgent(protocol.AgentClaude); configured != "" && configured != protocol.ClaudeDefaultModel {
			return u.modelName(configured)
		}
		return "Claude Code"
	}
	if u.models != nil {
		for _, entry := range u.models.models {
			if (model == entry.Model || model == entry.ID) && entry.DisplayName != "" {
				return entry.DisplayName
			}
		}
	}
	if protocol.AgentForModel(model) == protocol.AgentClaude {
		if name := protocol.ClaudeModelDisplayName(model); name != model {
			return name
		}
		return claudeModelPrettyName(strings.TrimPrefix(model, protocol.ClaudeModelPrefix))
	}
	return model
}

// claudeModelPrettyName turns IDs such as claude-opus-5-5 or
// claude-sonnet-4-5-20250929[1m] into "Claude Opus 5.5".
func claudeModelPrettyName(id string) string {
	id = strings.TrimPrefix(strings.TrimSpace(id), "claude-")
	suffix := ""
	if base, found := strings.CutSuffix(id, "[1m]"); found {
		id, suffix = base, " (1M)"
	}
	var words, numbers []string
	for _, part := range strings.Split(id, "-") {
		switch {
		case part == "":
		case strings.Trim(part, "0123456789") != "":
			words = append(words, strings.ToUpper(part[:1])+part[1:])
		case len(part) < 8: // skip date stamps such as 20250929
			numbers = append(numbers, part)
		}
	}
	name := strings.Join(append([]string{"Claude"}, words...), " ")
	if len(numbers) > 0 {
		name += " " + strings.Join(numbers, ".")
	}
	return name + suffix
}

// chatDefaultPickerChoices lists models for /default or /fallback, marking
// the current choice.
func chatDefaultPickerChoices(u *chatUI, selected int, kind string) []chatCompletionChoice {
	choices := chatModelPickerChoices(u.models, "", "", selected)
	current := u.pickerCurrentChoice(kind)
	for index := range choices {
		choice := &choices[index]
		if choice.Total == 0 {
			if choice.Value == "Choose a model" && kind == "fallback" {
				choice.Value, choice.Description = "Choose the fallback model", "Used when a provider hits its usage limit"
			} else if choice.Value == "Choose a model" {
				choice.Value, choice.Description = "Choose the default model", "For new conversations on this host"
			}
			continue
		}
		id, _, _ := modelOverride(u.models.models[choice.Index])
		choice.Description = strings.TrimSuffix(choice.Description, " · default")
		if id == current {
			choice.Description += " · " + kind
		}
	}
	return choices
}

// pickerCurrentChoice is the model /default or /fallback currently names.
func (u *chatUI) pickerCurrentChoice(kind string) string {
	if u.defaults == nil {
		return ""
	}
	if kind == "fallback" {
		return u.defaults.Fallback
	}
	if u.defaults.Default != "" {
		return u.defaults.Default
	}
	return u.defaults.ForAgent(u.defaults.Agent)
}
