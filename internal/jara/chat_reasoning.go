package jara

import (
	"fmt"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
)

func (u *chatUI) modelByID(id string) *protocol.CodexModel {
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

func chatSupportsReasoning(model *protocol.CodexModel, effort string) bool {
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

// chatReasoningOrder ranks effort levels across providers: Codex offers
// minimal and Claude Code offers max, so the scale spans both.
var chatReasoningOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// chatNearestReasoning keeps the effort when the model supports it and
// otherwise picks the closest supported level, preferring the lower one on a
// tie. Unknown efforts fall back to the model's default.
func chatNearestReasoning(model *protocol.CodexModel, effort string) string {
	if model == nil || chatSupportsReasoning(model, effort) {
		return effort
	}
	rank := func(value string) int {
		for index, level := range chatReasoningOrder {
			if level == value {
				return index
			}
		}
		return -1
	}
	want := rank(effort)
	best, bestDistance := model.DefaultReasoningEffort, len(chatReasoningOrder)
	if want < 0 {
		return best
	}
	for _, option := range model.SupportedReasoningEfforts {
		have := rank(option.Value)
		if have < 0 {
			continue
		}
		distance := have - want
		if distance < 0 {
			distance = -distance*2 - 1 // a lower level wins ties
		} else {
			distance *= 2
		}
		if distance < bestDistance {
			best, bestDistance = option.Value, distance
		}
	}
	return best
}

func (u *chatUI) reasoningModel() *protocol.CodexModel {
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

func (u *chatUI) canChooseChatReasoning() error {
	if !u.threadReady {
		return fmt.Errorf("open a conversation before selecting reasoning effort")
	}
	return nil
}

func (u *chatUI) stepChatReasoning(delta int) error {
	if err := u.canChooseChatReasoning(); err != nil {
		return err
	}
	if u.models == nil || u.models.failure != "" {
		if err := u.startChatModelList(); err != nil {
			return err
		}
	}
	if !u.models.complete {
		u.pendingReasoningStep += delta
		return nil
	}
	model := u.reasoningModel()
	if model == nil || len(model.SupportedReasoningEfforts) == 0 {
		return fmt.Errorf("the selected model does not advertise reasoning effort choices")
	}
	options := model.SupportedReasoningEfforts
	current := chatNearestReasoning(model, u.currentChatReasoning())
	index := 0
	for i, option := range options {
		if option.Value == current {
			index = i
			break
		}
	}
	effort := options[min(len(options)-1, max(0, index+delta))].Value
	if effort == current {
		return nil
	}
	return u.chooseChatReasoning(effort)
}

func (u *chatUI) chooseChatReasoning(value string) error {
	if err := u.canChooseChatReasoning(); err != nil {
		return err
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
	if err := u.publishChatTurnSelection(u.nextModel, value); err != nil {
		return err
	}
	u.nextReasoning = value
	u.reasoningSelectionGeneration++
	return nil
}

func (u *chatUI) modelControlLabel() string {
	if u.nextModel != "" {
		return u.nextModelLabel
	}
	if u.effectiveModel != "" {
		return u.effectiveModelLabel()
	}
	return u.newConversationModelLabel()
}

func (u *chatUI) currentChatReasoning() string {
	if u.nextReasoning != "" {
		return u.nextReasoning
	}
	if u.effectiveReasoning != "" {
		return u.effectiveReasoning
	}
	if model := u.reasoningModel(); model != nil {
		return model.DefaultReasoningEffort
	}
	return ""
}

func (u *chatUI) reasoningControlLabel() string {
	value := u.currentChatReasoning()
	if value == "" {
		return "Default reasoning" // beside the model, plain "Default" would read as its name
	}
	label := chatReasoningEffortLabel(value)
	if u.nextReasoning == "" && u.effectiveReasoning == "" {
		return label + " · default"
	}
	return label
}

func chatModelDisplayName(model *protocol.CodexModel) string {
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
