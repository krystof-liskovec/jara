package jara

import (
	"fmt"
	"strconv"
	"strings"
)

func (u *chatUI) modelByID(id string) *codexModel {
	if u.models == nil {
		return nil
	}
	for index := range u.models.models {
		model := &u.models.models[index]
		if model.ID == id || model.Model == id {
			return model
		}
	}
	return nil
}

func chatSupportsReasoning(model *codexModel, effort string) bool {
	if model == nil || effort == "" {
		return false
	}
	for _, option := range model.SupportedReasoningEfforts {
		if option.Value == effort {
			return true
		}
	}
	return false
}

func (u *chatUI) reasoningModel() *codexModel {
	modelID := u.nextModel
	if modelID == "" {
		modelID = u.effectiveModel
	}
	if modelID != "" {
		return u.modelByID(modelID)
	}
	if u.models != nil {
		for index := range u.models.models {
			if u.models.models[index].IsDefault {
				return &u.models.models[index]
			}
		}
	}
	return nil
}

func (u *chatUI) chooseChatReasoning(value string) error {
	if u.busy || u.turn != "" {
		return fmt.Errorf("wait for the current turn to finish before changing reasoning effort")
	}
	if !u.threadReady {
		return fmt.Errorf("open a conversation before selecting reasoning effort")
	}
	if u.models == nil || !u.models.complete {
		return fmt.Errorf("load the model list before selecting reasoning effort")
	}
	model := u.reasoningModel()
	if model == nil || len(model.SupportedReasoningEfforts) == 0 {
		return fmt.Errorf("the selected model does not advertise reasoning effort choices")
	}
	value = strings.TrimSpace(value)
	if number, err := strconv.Atoi(value); err == nil {
		if number < 1 || number > len(model.SupportedReasoningEfforts) {
			return fmt.Errorf("choose a reasoning level from 1 to %d", len(model.SupportedReasoningEfforts))
		}
		value = model.SupportedReasoningEfforts[number-1].Value
	} else {
		for _, option := range model.SupportedReasoningEfforts {
			if strings.EqualFold(value, option.Value) || strings.EqualFold(value, chatReasoningEffortLabel(option.Value)) {
				value = option.Value
				break
			}
		}
	}
	if !chatSupportsReasoning(model, value) {
		return fmt.Errorf("reasoning effort %q is not supported by %s", value, chatModelDisplayName(model))
	}
	u.nextReasoning = value
	u.reasoningSelectionGeneration++
	return nil
}

func (u *chatUI) modelControlLabel() string {
	if u.nextModel != "" {
		return u.nextModelLabel + " · next turn"
	}
	if u.effectiveModel != "" {
		return u.effectiveModelLabel()
	}
	return u.newConversationModelLabel()
}

func (u *chatUI) reasoningControlLabel() string {
	value := u.nextReasoning
	if value == "" {
		value = u.effectiveReasoning
	}
	if value == "" {
		if model := u.reasoningModel(); model != nil {
			value = model.DefaultReasoningEffort
		}
	}
	if value == "" {
		return "Default"
	}
	label := chatReasoningEffortLabel(value)
	if u.nextReasoning != "" {
		return label + " · next turn"
	}
	if u.effectiveReasoning == "" {
		return label + " · default"
	}
	return label
}

func chatModelDisplayName(model *codexModel) string {
	if model == nil {
		return "the selected model"
	}
	if model.DisplayName != "" {
		return model.DisplayName
	}
	if model.Model != "" {
		return model.Model
	}
	return model.ID
}

func chatReasoningEffortLabel(value string) string {
	switch strings.ToLower(value) {
	case "xhigh":
		return "Extra high"
	case "max":
		return "Maximum"
	case "none":
		return "None"
	case "low", "medium", "high", "minimal", "ultra":
		return strings.ToUpper(value[:1]) + value[1:]
	default:
		return value
	}
}
