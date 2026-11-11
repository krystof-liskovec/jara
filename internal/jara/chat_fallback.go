package jara

import (
	"encoding/json"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/textutil"
)

// /fallback names a model, usually from the other provider, that takes over
// when the current one hits its usage limit: /crunch switches to it instead of
// waiting, and an ordinary turn is retried on it once.

func isChatFallbackCommand(line string) bool {
	line = strings.TrimSpace(line)
	return line == "/fallback" || strings.HasPrefix(line, "/fallback ") || strings.HasPrefix(line, "/fallback\t")
}

func (u *chatUI) runChatFallbackCommand(line string) error {
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/fallback"))
	switch {
	case value == "" && chatUIIsInteractive(u):
		u.pickerRequested = "fallback"
		if u.models != nil && u.models.complete && u.models.failure == "" {
			return nil
		}
		if err := u.startChatModelList(); err != nil {
			u.pickerRequested = ""
			u.print("\n" + err.Error() + "\n")
		}
		return nil
	case value == "":
		current := "off"
		if u.defaults != nil && u.defaults.Fallback != "" {
			current = u.modelName(u.defaults.Fallback)
		}
		u.print("Fallback model: " + current + ".\nUse /fallback ID, /fallback N, or /fallback off.\n")
		return nil
	case strings.EqualFold(value, "off"):
		return u.setChatFallbackModel("")
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
	return u.setChatFallbackModel(id)
}

func (u *chatUI) setChatFallbackModel(model string) error {
	id, err := u.c.request("jara/fallbackModel/set", map[string]string{"model": model})
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{method: "jara/fallbackModel/set", deadline: time.Now().Add(time.Minute), action: model}
	return nil
}

// retryOnFallback resends the prompt of a turn that ended at a usage limit
// on the fallback model. It retries once per prompt and never onto the model
// that just failed.
func (u *chatUI) retryOnFallback(params json.RawMessage) (bool, error) {
	var event struct {
		Turn struct {
			Error *struct {
				Message        string          `json:"message"`
				CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &event) != nil || event.Turn.Error == nil || u.defaults == nil {
		return false, nil
	}
	fallback, prompt := u.defaults.Fallback, u.lastPrompt
	current := textutil.FirstNonEmpty(u.nextModel, u.effectiveModel)
	if fallback == "" || prompt == "" || u.fallbackRetried || fallback == current || !u.threadReady ||
		!chatCrunchIsUsageLimit(event.Turn.Error.CodexErrorInfo, event.Turn.Error.Message) {
		return false, nil
	}
	if err := u.chooseChatModel(fallback); err != nil {
		return false, nil
	}
	u.printRole("system", u.modelName(current)+" reached its usage limit; continuing on "+u.modelName(fallback)+".\n")
	u.fallbackRetried = true
	return true, u.sendPrompt(prompt)
}
