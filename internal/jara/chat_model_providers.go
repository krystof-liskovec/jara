package jara

import (
	"slices"
	"strconv"

	"krystof.liskovec/jara/internal/protocol"
)

// The model picker opens on the providers, the agents that run the models,
// and then lists the chosen provider's models; Escape goes back to the
// providers. With models from one provider only, it lists them directly.

func chatModelProvider(model protocol.CodexModel) string {
	id, _, _ := modelOverride(model)
	return protocol.AgentForModel(id)
}

// chatModelProviders lists the providers with models, in the list's order.
func chatModelProviders(models []protocol.CodexModel) []string {
	var providers []string
	for _, model := range models {
		if provider := chatModelProvider(model); !slices.Contains(providers, provider) {
			providers = append(providers, provider)
		}
	}
	return providers
}

// chatProviderModels returns where one provider's models sit in models; an
// empty provider keeps them all.
func chatProviderModels(models []protocol.CodexModel, provider string) []int {
	var indexes []int
	for index, model := range models {
		if provider == "" || chatModelProvider(model) == provider {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

// showsProviders reports whether the model picker lists providers now.
func (p *chatTerminalPicker) showsProviders(u *chatUI) bool {
	return p.kind == "model" && p.provider == "" && u.models != nil && u.models.complete &&
		len(chatModelProviders(u.models.models)) > 1
}

// selectModel points the model picker at the model with index in the list,
// or at the first one for -1: at its provider while the providers show, else
// at it among its provider's models.
func (p *chatTerminalPicker) selectModel(u *chatUI, index int) {
	models := u.models.models
	providers := chatModelProviders(models)
	if len(providers) == 1 {
		p.provider = providers[0]
	}
	if p.provider == "" {
		p.selected = 0
		if index >= 0 {
			p.selected = max(0, slices.Index(providers, chatModelProvider(models[index])))
		}
		return
	}
	p.selected = max(0, slices.Index(chatProviderModels(models, p.provider), index))
}

// chooseModel opens the selected provider, or picks the selected model.
func (p *chatTerminalPicker) chooseModel(u *chatUI) error {
	if u.models == nil {
		return nil
	}
	if p.showsProviders(u) {
		providers := chatModelProviders(u.models.models)
		if p.selected >= len(providers) {
			return nil
		}
		p.provider = providers[p.selected]
		p.selectModel(u, u.pickerModelIndex("model"))
		return nil
	}
	indexes := chatProviderModels(u.models.models, p.provider)
	if p.selected >= len(indexes) {
		return nil
	}
	if err := u.chooseChatModel(strconv.Itoa(indexes[p.selected] + 1)); err != nil {
		return err
	}
	u.print("Model set to " + u.nextModelLabel + " for the next and subsequent turns.\n")
	p.close()
	return nil
}

// back returns from a provider's models to the providers and reports
// whether it did.
func (p *chatTerminalPicker) back(u *chatUI) bool {
	if p.kind != "model" || p.provider == "" || u.models == nil {
		return false
	}
	providers := chatModelProviders(u.models.models)
	if len(providers) < 2 {
		return false
	}
	p.selected, p.provider = max(0, slices.Index(providers, p.provider)), ""
	return true
}

// chatProviderPickerChoices lists the providers, marking the current model's.
func chatProviderPickerChoices(u *chatUI, selected int) []chatCompletionChoice {
	providers := chatModelProviders(u.models.models)
	current := ""
	if index := u.pickerModelIndex("model"); index >= 0 {
		current = chatModelProvider(u.models.models[index])
	}
	choices := []chatCompletionChoice{{Value: "Choose a provider", Description: "Then one of its models"}}
	for index, provider := range providers {
		count := len(chatProviderModels(u.models.models, provider))
		description := strconv.Itoa(count) + " models"
		if count == 1 {
			description = "1 model"
		}
		if provider == current {
			description += " · current"
		}
		choices = append(choices, chatCompletionChoice{
			Value: protocol.AgentDisplayName(provider), Description: description,
			Selected: index == selected, Index: index, Total: len(providers),
		})
	}
	return choices
}

// pickerModelIndex finds the model a picker starts on: the next or current
// model, or the one /default or /fallback names, else the default; -1 when
// none is listed.
func (u *chatUI) pickerModelIndex(kind string) int {
	if u.models == nil {
		return -1
	}
	current := u.nextModel
	if current == "" {
		current = u.effectiveModel
	}
	if kind != "model" {
		current = u.pickerCurrentChoice(kind)
	}
	for index, model := range u.models.models {
		id, _, _ := modelOverride(model)
		if current != "" && (current == id || current == model.ID || current == model.Model) || current == "" && model.IsDefault {
			return index
		}
	}
	return -1
}
