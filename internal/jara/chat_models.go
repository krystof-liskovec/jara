package jara

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	chatModelPageSize   = 100
	chatModelMaxPages   = 2
	chatModelMaxShown   = 12
	chatModelMaxIDBytes = 128
)

type codexModel struct {
	ID                        string                      `json:"id"`
	Model                     string                      `json:"model"`
	DisplayName               string                      `json:"displayName"`
	Hidden                    bool                        `json:"hidden"`
	IsDefault                 bool                        `json:"isDefault"`
	DefaultReasoningEffort    string                      `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []chatReasoningEffortOption `json:"supportedReasoningEfforts"`
}

type chatReasoningEffortOption struct {
	Value       string `json:"reasoningEffort"`
	Description string `json:"description"`
}

type chatModelBrowser struct {
	cursor     string
	pages      int
	models     []codexModel
	seen       map[string]struct{}
	complete   bool
	incomplete bool
	more       bool
	failure    string
}

type chatTurnRequest struct {
	model            string
	effort           string
	modelGeneration  uint64
	effortGeneration uint64
	thread           string
}

func newChatModelBrowser() *chatModelBrowser {
	return &chatModelBrowser{seen: map[string]struct{}{}}
}

func (b *chatModelBrowser) requestParams() map[string]any {
	params := map[string]any{"limit": chatModelPageSize, "includeHidden": false}
	if b.cursor != "" {
		params["cursor"] = b.cursor
	}
	return params
}

func (b *chatModelBrowser) acceptPage(raw json.RawMessage) (bool, error) {
	if b.complete {
		return false, fmt.Errorf("model list is already complete")
	}
	var page struct {
		Data       []codexModel `json:"data"`
		NextCursor *string      `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return false, fmt.Errorf("decode model list: %w", err)
	}
	if page.Data == nil {
		return false, fmt.Errorf("decode model list: response is missing data")
	}
	b.pages++
	for _, model := range page.Data {
		if model.Hidden || model.ID == "" && model.Model == "" {
			continue
		}
		key := model.ID
		if key == "" {
			key = model.Model
		}
		if _, duplicate := b.seen[key]; duplicate {
			continue
		}
		b.seen[key] = struct{}{}
		if len(b.models) < chatModelMaxShown {
			b.models = append(b.models, model)
		} else {
			b.more = true
		}
	}
	if page.NextCursor == nil || *page.NextCursor == "" {
		b.complete = true
		return false, nil
	}
	if b.more {
		b.complete = true
		return false, nil
	}
	if *page.NextCursor == b.cursor {
		return false, fmt.Errorf("Codex returned a repeated model-list cursor")
	}
	if b.pages >= chatModelMaxPages {
		b.incomplete = true
		b.complete = true
		return false, nil
	}
	b.cursor = *page.NextCursor
	return true, nil
}

func (b *chatModelBrowser) fail(err error) string {
	b.complete = true
	b.incomplete = true
	if err != nil {
		b.failure = chatText(err.Error())
	}
	return b.render()
}

func (b *chatModelBrowser) model(value string) (id, label string, err error) {
	value = strings.TrimSpace(value)
	if numericChatModelChoice(value) {
		if !b.complete {
			return "", "", fmt.Errorf("model list is still loading; wait for /models before choosing a number")
		}
		number, parseErr := strconv.Atoi(value)
		if parseErr != nil || number < 1 || number > len(b.models) {
			if len(b.models) == 0 {
				return "", "", fmt.Errorf("no listed models to choose; run /models first")
			}
			return "", "", fmt.Errorf("choose a listed model number from 1 to %d", len(b.models))
		}
		return modelOverride(b.models[number-1])
	}
	if !validChatModelID(value) {
		return "", "", fmt.Errorf("model ID must be 1 to %d ASCII letters, digits, '.', '_', '-', or '/'", chatModelMaxIDBytes)
	}
	for _, model := range b.models {
		if value == model.ID || value == model.Model {
			return modelOverride(model)
		}
	}
	return value, value, nil
}

func modelOverride(model codexModel) (id, label string, err error) {
	id = model.Model
	if id == "" {
		id = model.ID
	}
	if !validChatModelID(id) {
		return "", "", fmt.Errorf("model catalog returned an invalid model ID")
	}
	label = model.DisplayName
	if label == "" {
		label = id
	}
	return id, label, nil
}

func validChatModelID(value string) bool {
	if value == "" || len(value) > chatModelMaxIDBytes {
		return false
	}
	for _, r := range value {
		if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func numericChatModelChoice(value string) bool {
	return numericChatSessionChoice(value)
}

func (b *chatModelBrowser) render() string {
	var out strings.Builder
	out.WriteString("Available Codex models\n")
	for i, model := range b.models {
		id := model.ID
		if id == "" {
			id = model.Model
		}
		label := model.DisplayName
		if label == "" {
			label = id
		}
		fmt.Fprintf(&out, "%d. %s (%s)", i+1, compactChatModelText(label, 40), compactChatModelText(id, 48))
		if model.IsDefault {
			out.WriteString(" · default")
		}
		out.WriteByte('\n')
	}
	if len(b.models) == 0 {
		if b.incomplete {
			out.WriteString("No models in the results received so far.\n")
		} else {
			out.WriteString("No models are available.\n")
		}
	}
	if b.more {
		out.WriteString("Showing the first 12 models; use /model ID to select another model.\n")
	}
	if b.incomplete {
		out.WriteString("Model list is incomplete.\n")
	}
	if b.failure != "" {
		fmt.Fprintf(&out, "Model lookup failed: %s\n", compactChatModelText(b.failure, 160))
	}
	if len(b.models) > 0 {
		out.WriteString("Use /model N or /model ID to select a model for this conversation.\n")
	}
	return out.String()
}

func compactChatModelText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(chatText(value)), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

func (u *chatUI) startChatModelList() error {
	if !u.connected {
		return fmt.Errorf("wait for the Codex connection before listing models")
	}
	browser := newChatModelBrowser()
	for id, pending := range u.pending {
		if pending.method == "model/list" {
			delete(u.pending, id)
		}
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.models = browser
	if err := u.request("model/list", browser.requestParams()); err != nil {
		u.models = nil
		return err
	}
	return nil
}

func (u *chatUI) acceptChatModelPage(raw json.RawMessage) error {
	if u.models == nil {
		return nil
	}
	more, err := u.models.acceptPage(raw)
	if err != nil {
		u.finishChatModelList(err)
		return nil
	}
	if more {
		if err := u.request("model/list", u.models.requestParams()); err != nil {
			u.finishChatModelList(err)
		}
		return nil
	}
	u.printChatModels()
	return nil
}

func (u *chatUI) failChatModelRequest(err error) bool {
	if u.models == nil {
		return false
	}
	for id, pending := range u.pending {
		if pending.method == "model/list" {
			delete(u.pending, id)
		}
	}
	u.finishChatModelList(err)
	return true
}

func (u *chatUI) expireChatModelRequest(id string) bool {
	pending, ok := u.pending[id]
	if !ok || pending.method != "model/list" {
		return false
	}
	delete(u.pending, id)
	return u.failChatModelRequest(fmt.Errorf("Codex app-server timed out during model/list"))
}

func (u *chatUI) finishChatModelList(err error) {
	if u.models == nil {
		return
	}
	u.pickerRequested = ""
	u.print("\n" + u.models.fail(err))
	if !u.busy {
		u.print("You> ")
	}
}

func (u *chatUI) printChatModels() {
	if u.models == nil {
		return
	}
	if u.pickerRequested != "" && chatUIIsInteractive(u) {
		return
	}
	u.print("\n" + u.models.render())
	if !u.busy {
		u.print("You> ")
	}
}

func (u *chatUI) chooseChatModel(value string) error {
	if u.busy || u.turn != "" {
		return fmt.Errorf("wait for the current turn to finish before changing models")
	}
	if !u.threadReady {
		return fmt.Errorf("open a conversation before selecting its model")
	}
	if u.models == nil {
		u.models = newChatModelBrowser()
		u.models.complete = true
	}
	model, label, err := u.models.model(value)
	if err != nil {
		return err
	}
	u.nextModel = model
	u.nextModelLabel = label
	u.modelSelectionGeneration++
	catalogModel := u.modelByID(model)
	if catalogModel == nil {
		if u.nextReasoning != "" {
			u.nextReasoning = ""
			u.reasoningSelectionGeneration++
		}
	} else {
		effort := u.nextReasoning
		if effort == "" {
			effort = u.effectiveReasoning
		}
		if !chatSupportsReasoning(catalogModel, effort) {
			effort = catalogModel.DefaultReasoningEffort
		}
		if effort != u.nextReasoning {
			u.nextReasoning = effort
			u.reasoningSelectionGeneration++
		}
	}
	return nil
}

func (u *chatUI) requestTurn(params map[string]any) error {
	return u.requestTurnWithQueuedPrompt(params, "")
}

func (u *chatUI) requestTurnWithQueuedPrompt(params map[string]any, queuedPrompt string) error {
	params["approvalsReviewer"] = u.approvalsReviewer()
	id, err := u.c.request("turn/start", params)
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{method: "turn/start", deadline: time.Now().Add(30 * time.Second), threadID: u.thread, queuedPrompt: queuedPrompt}
	if u.pendingTurnModels == nil {
		u.pendingTurnModels = map[string]chatTurnRequest{}
	}
	request := chatTurnRequest{thread: u.thread}
	if u.nextModel != "" {
		request.model = u.nextModel
		request.modelGeneration = u.modelSelectionGeneration
	}
	if u.nextReasoning != "" {
		request.effort = u.nextReasoning
		request.effortGeneration = u.reasoningSelectionGeneration
	}
	u.pendingTurnModels[id] = request
	return nil
}

func (u *chatUI) setEffectiveModel(model string) {
	model = strings.TrimSpace(chatText(model))
	if model != u.effectiveModel {
		u.resolvedModel = ""
	}
	u.effectiveModel = model
	if writer, ok := u.out.(interface{ SetChatAgent(string) }); ok {
		writer.SetChatAgent(agentDisplayName(agentForModel(u.effectiveModel)))
	}
}

func (u *chatUI) effectiveModelLabel() string {
	if u.effectiveModel == "" {
		return "not reported by Codex"
	}
	if u.resolvedModel != "" && agentForModel(u.effectiveModel) == agentClaude {
		return claudeModelPrettyName(u.resolvedModel)
	}
	return u.modelName(u.effectiveModel)
}
