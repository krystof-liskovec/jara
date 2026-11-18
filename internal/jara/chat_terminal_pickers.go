package jara

import (
	"fmt"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
)

type chatTerminalPicker struct {
	kind     string
	selected int
	provider string // the model picker's chosen provider; "" lists the providers
}

func chatSessionPickerChoices(browser *chatSessionBrowser, selected int) []chatCompletionChoice {
	if browser == nil {
		return nil
	}
	if !browser.complete {
		description := ""
		if len(browser.matches) > 0 {
			description = strconv.Itoa(len(browser.matches)) + " found"
		}
		return []chatCompletionChoice{{Value: "Loading recent sessions…", Description: description}}
	}
	if len(browser.matches) == 0 {
		return []chatCompletionChoice{{Value: "No recent sessions found", Description: "Esc closes"}}
	}
	choices := make([]chatCompletionChoice, 0, len(browser.matches)+1)
	description := "newest first"
	if browser.more {
		description = "newest 8 · /sessions query for more"
	}
	choices = append(choices, chatCompletionChoice{Value: "Recent sessions", Description: description})
	for index, session := range browser.matches {
		title := session.Name
		if strings.TrimSpace(title) == "" {
			title = session.Preview
		}
		if strings.TrimSpace(title) == "" {
			title = session.ID
		}
		choices = append(choices, chatCompletionChoice{
			Value:       strconv.Itoa(index+1) + ". " + compactChatSessionText(title, chatSessionTitleRunes),
			Description: "updated " + conversationAge(max(session.Updated, session.Created)),
			Selected:    index == selected,
			Index:       index,
			Total:       len(browser.matches),
		})
	}
	return choices
}

func (p *chatTerminalPicker) request(u *chatUI, kind string) error {
	p.kind, p.selected, p.provider = kind, 0, ""
	if u.models != nil && u.models.complete && u.models.failure == "" {
		p.selectCurrent(u)
		return nil
	}
	u.pickerRequested = kind
	if err := u.startChatModelList(); err != nil {
		p.close()
		return err
	}
	return nil
}

func (p *chatTerminalPicker) sync(u *chatUI) {
	if u.pickerRequested == "rewind" { // lists this conversation's prompts, no model list needed
		p.kind, p.selected, u.pickerRequested = "rewind", max(0, len(u.promptLog)-1), ""
		return
	}
	if p.kind == "rewind" {
		return
	}
	if u.pickerRequested != "" {
		p.kind = u.pickerRequested
	}
	if u.models == nil || !u.models.complete {
		return
	}
	if u.models.failure != "" {
		p.close()
		return
	}
	if u.pickerRequested != "" {
		p.kind, p.selected, p.provider = u.pickerRequested, 0, ""
		u.pickerRequested = ""
		p.selectCurrent(u)
	}
}

func (p *chatTerminalPicker) close() { p.kind, p.selected, p.provider = "", 0, "" }

func (p *chatTerminalPicker) active() bool { return p.kind != "" }

func (p *chatTerminalPicker) selectCurrent(u *chatUI) {
	p.selected = 0
	switch p.kind {
	case "model", "default", "fallback":
		if u.models == nil {
			return
		}
		index := u.pickerModelIndex(p.kind)
		if p.kind == "model" {
			p.selectModel(u, index)
		} else {
			p.selected = max(0, index)
		}
	case "reasoning":
		model := u.reasoningModel()
		if model == nil {
			return
		}
		current := u.currentChatReasoning()
		for index, option := range model.SupportedReasoningEfforts {
			if option.Value == current {
				p.selected = index
				return
			}
		}
	}
}

func (p *chatTerminalPicker) choices(u *chatUI) []chatCompletionChoice {
	switch p.kind {
	case "model":
		if p.showsProviders(u) {
			return chatProviderPickerChoices(u, p.selected)
		}
		return chatModelPickerChoices(u.models, u.nextModel, u.effectiveModel, p.selected, p.provider)
	case "default", "fallback":
		return chatDefaultPickerChoices(u, p.selected, p.kind)
	case "reasoning":
		return chatReasoningPickerChoices(u, p.selected)
	case "rewind":
		return chatRewindPickerChoices(u, p.selected)
	default:
		return nil
	}
}

func (p *chatTerminalPicker) move(u *chatUI, delta int) {
	choices := p.choices(u)
	selectable := 0
	for _, choice := range choices {
		if choice.Total > 0 {
			selectable = choice.Total
			break
		}
	}
	if selectable == 0 {
		return
	}
	p.selected = min(selectable-1, max(0, p.selected+delta))
}

func (p *chatTerminalPicker) choose(u *chatUI) error {
	switch p.kind {
	case "rewind":
		index := p.selected
		p.close()
		return u.rewindTo(index)
	case "model":
		return p.chooseModel(u)
	case "default", "fallback":
		if u.models == nil || p.selected >= len(u.models.models) {
			return nil
		}
		id, _, err := modelOverride(u.models.models[p.selected])
		if err != nil {
			return err
		}
		kind := p.kind
		p.close()
		if kind == "fallback" {
			return u.setChatFallbackModel(id)
		}
		return u.setChatDefaultModel(id)
	case "reasoning":
		model := u.reasoningModel()
		if model == nil || p.selected >= len(model.SupportedReasoningEfforts) {
			return nil
		}
		effort := model.SupportedReasoningEfforts[p.selected].Value
		if err := u.chooseChatReasoning(effort); err != nil {
			return err
		}
		u.print("Reasoning set to " + chatReasoningEffortLabel(u.nextReasoning) + " for the next and subsequent turns.\n")
		p.close()
	}
	return nil
}

// chatModelPickerChoices lists the models of one provider, or every model
// for an empty provider.
func chatModelPickerChoices(browser *chatModelBrowser, next, current string, selected int, provider string) []chatCompletionChoice {
	if browser == nil {
		return []chatCompletionChoice{{Value: "Loading models…"}}
	}
	if !browser.complete {
		return []chatCompletionChoice{{Value: "Loading models…"}}
	}
	if len(browser.models) == 0 {
		return []chatCompletionChoice{{Value: "No models found", Description: "Esc closes"}}
	}
	heading := chatCompletionChoice{Value: "Choose a model", Description: "For this conversation"}
	if provider != "" && len(chatModelProviders(browser.models)) > 1 {
		heading = chatCompletionChoice{Value: protocol.AgentDisplayName(provider) + " models", Description: "Esc goes back to the providers"}
	}
	choices := []chatCompletionChoice{heading}
	indexes := chatProviderModels(browser.models, provider)
	for index, at := range indexes {
		model := &browser.models[at]
		id, label, _ := modelOverride(*model)
		description := id
		if model.IsDefault {
			description += " · default"
		}
		if next != "" && (id == next || model.ID == next) {
			description += " · current" // a switch takes over from the running model
		} else if current != "" && (id == current || model.ID == current) {
			description += " · current"
		}
		choices = append(choices, chatCompletionChoice{
			Value: label, Description: description, Selected: index == selected,
			Index: index, Total: len(indexes),
		})
	}
	if browser.more && provider != protocol.AgentClaude { // further pages list Codex models
		choices = append(choices, chatCompletionChoice{Value: "More models available", Description: "Use /model ID to select one"})
	}
	return choices
}

func chatReasoningPickerChoices(u *chatUI, selected int) []chatCompletionChoice {
	model := u.reasoningModel()
	if model == nil {
		return []chatCompletionChoice{{Value: "Choose a listed model first", Description: "F2 opens model selection"}}
	}
	if len(model.SupportedReasoningEfforts) == 0 {
		return []chatCompletionChoice{{Value: "No reasoning choices", Description: chatModelDisplayName(model)}}
	}
	choices := []chatCompletionChoice{{Value: "Reasoning effort", Description: chatModelDisplayName(model)}}
	active := u.currentChatReasoning()
	for index, option := range model.SupportedReasoningEfforts {
		description := strings.TrimSpace(option.Description)
		if description == "" {
			description = "Reasoning effort"
		}
		if option.Value == active {
			description += " · selected"
		}
		choices = append(choices, chatCompletionChoice{
			Value: chatReasoningEffortLabel(option.Value), Description: description,
			Selected: index == selected, Index: index, Total: len(model.SupportedReasoningEfforts),
		})
	}
	return choices
}

func chatPickerErrorText(kind string, err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", kind, err.Error())
}
