package jara

import (
	"encoding/json"
	"strings"
	"time"
)

func chatInitializeParams() map[string]any {
	return map[string]any{
		"clientInfo":   map[string]string{"name": "jara", "title": "Jara", "version": Version},
		"capabilities": map[string]bool{"experimentalApi": true},
	}
}

func isChatPlanCommand(line string) bool {
	line = strings.TrimSpace(line)
	return line == "/plan" || strings.HasPrefix(line, "/plan ") || strings.HasPrefix(line, "/plan\t")
}

func (u *chatUI) runChatPlanCommand(line string) error {
	argument := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/plan"))
	mode, prompt := "plan", ""
	switch {
	case strings.EqualFold(argument, "status"):
		state := "Default"
		if u.planMode {
			state = "Plan"
		}
		u.print("Conversation mode: " + state + ".\n" + u.chatPlanPrompt())
		return nil
	case strings.EqualFold(argument, "off"), strings.EqualFold(argument, "default"):
		mode = "default"
	case argument == "", strings.EqualFold(argument, "on"):
	case argument != "":
		prompt = argument
	}
	if !u.threadReady {
		u.print("No conversation is open. Use /resume ID or /new first.\n" + u.chatPlanPrompt())
		return nil
	}
	if u.busy {
		u.print("A turn is running. Wait for it to finish before changing conversation mode.\n" + u.chatPlanPrompt())
		return nil
	}
	if u.composing && prompt != "" {
		u.print("Send or discard the composed message before starting a Plan prompt.\nCompose> ")
		return nil
	}
	if u.chatPlanUpdatePending() {
		u.print("A conversation mode change is still pending.\n" + u.chatPlanPrompt())
		return nil
	}
	model := u.nextModel
	if model == "" {
		model = u.effectiveModel
	}
	if model == "" {
		u.print("Codex has not reported the active model yet. Try /plan again after the conversation opens.\n" + u.chatPlanPrompt())
		return nil
	}
	effort := u.nextReasoning
	if effort == "" {
		effort = u.effectiveReasoning
	}
	var effortValue any
	if effort != "" {
		effortValue = effort
	}
	params := map[string]any{
		"threadId": u.thread,
		"collaborationMode": map[string]any{
			"mode": mode,
			"settings": map[string]any{
				"model": model, "reasoningEffort": effortValue, "developerInstructions": nil,
			},
		},
	}
	id, err := u.c.request("thread/settings/update", params)
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{
		method: "thread/settings/update", deadline: time.Now().Add(30 * time.Second),
		action: mode, threadID: u.thread, queuedPrompt: prompt,
	}
	u.print("Updating conversation mode…\n")
	return nil
}

func (u *chatUI) acceptChatPlanResponse(message appMessage, pending chatPending) error {
	if message.Error != nil {
		u.print("Conversation mode update failed: " + chatText(message.Error.Message) + "\n" + u.chatPlanPrompt())
		return nil
	}
	u.planMode = pending.action == "plan"
	if u.planMode {
		u.print("Plan mode enabled for this conversation.\n")
	} else {
		u.print("Default mode enabled for this conversation.\n")
	}
	if pending.queuedPrompt != "" {
		return u.sendPrompt(pending.queuedPrompt)
	}
	started, err := u.dispatchQueuedPrompt()
	if err != nil {
		return err
	}
	if !started {
		u.print(u.chatPlanPrompt())
	}
	return nil
}

func (u *chatUI) acceptChatPlanUpdate(raw json.RawMessage) error {
	var update struct {
		ThreadID       string `json:"threadId"`
		ThreadSettings struct {
			CollaborationMode struct {
				Mode string `json:"mode"`
			} `json:"collaborationMode"`
		} `json:"threadSettings"`
	}
	if err := json.Unmarshal(raw, &update); err != nil {
		return err
	}
	if update.ThreadID == u.thread {
		u.planMode = update.ThreadSettings.CollaborationMode.Mode == "plan"
	}
	return nil
}

func (u *chatUI) handleChatPlanItem(method string, item chatHistoryItem) {
	if item.Type != "plan" {
		return
	}
	if method == "item/started" {
		u.setActivityLabel("Planning")
		return
	}
	if method == "item/completed" {
		u.setActivityLabel("Thinking")
		if item.Text != "" {
			u.printRoleAt("assistant", "Plan:\n"+item.Text+"\n", chatHistoryItemTime(item, time.Now()))
		}
	}
}

func (u *chatUI) chatPlanPrompt() string {
	if u.composing {
		return "Compose> "
	}
	return "You> "
}

func (u *chatUI) chatPlanUpdatePending() bool {
	for _, pending := range u.pending {
		if pending.method == "thread/settings/update" {
			return true
		}
	}
	return false
}
