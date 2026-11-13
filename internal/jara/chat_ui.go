package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type chatPending struct {
	method       string
	deadline     time.Time
	action       string
	threadID     string
	queuedPrompt string
}

// Text from the model and tools is untrusted terminal content.
func chatText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 32 && r != 127 && !(r >= 128 && r <= 159) {
			return r
		}
		return -1
	}, s)
}
func (u *chatUI) print(s string) { fmt.Fprint(u.out, chatText(s)) }
func chatUIIsInteractive(u *chatUI) bool {
	marker, ok := u.out.(interface{ InteractiveChat() bool })
	return ok && marker.InteractiveChat()
}
func (u *chatUI) printRole(role, s string) {
	s = chatText(s)
	if writer, ok := u.out.(interface{ WriteChatRole(string, string) }); ok {
		writer.WriteChatRole(role, s)
		return
	}
	fmt.Fprint(u.out, s)
}
func (u *chatUI) printRoleAt(role, s string, at time.Time) {
	s = chatText(s)
	if writer, ok := u.out.(interface {
		WriteChatRoleAt(string, string, time.Time)
	}); ok {
		writer.WriteChatRoleAt(role, s, at)
		return
	}
	u.printRole(role, s)
}
func (u *chatUI) recordRole(role, s string) {
	if writer, ok := u.out.(interface{ WriteChatRole(string, string) }); ok {
		writer.WriteChatRole(role, chatText(s))
	}
}
func (u *chatUI) request(method string, params any) error {
	return u.requestWithAction(method, params, "")
}
func (u *chatUI) requestWithAction(method string, params any, action string) error {
	if u.chatOffline() {
		u.printRole("system", "Not connected to the Jara session yet; try again once it reconnects.\n")
		return nil
	}
	id, err := u.c.request(method, params)
	if err != nil {
		return err
	}
	pending := chatPending{method: method, deadline: time.Now().Add(30 * time.Second), action: action}
	if chatGoalRequest(method) {
		pending.threadID = u.thread
	}
	u.pending[id] = pending
	return nil
}
func (u *chatUI) setAgentState(state string) {
	if state == "resuming" {
		state = "starting"
	}
	if u.lastAgentState == state {
		return
	}
	u.lastAgentState = state
	if u.trackAgentState {
		publishChatAgentState(state)
	}
}
func publishChatAgentState(state string) {
	if os.Getenv("TMUX") == "" {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return
	}
	_ = tmux("set-option", "-t", pane, "@jara_agent_pid", strconv.Itoa(os.Getpid())).Run()
	_ = tmux("set-option", "-t", pane, "@jara_agent_state", state).Run()
}
func clearChatAgentState() {
	if os.Getenv("TMUX") == "" {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return
	}
	_ = tmux("set-option", "-u", "-t", pane, "@jara_agent_state").Run()
	_ = tmux("set-option", "-u", "-t", pane, "@jara_agent_pid").Run()
}
func (u *chatUI) open(id string) error {
	u.busy = true
	u.turnStartedAt = time.Time{}
	u.setActivityLabel("")
	u.setAgentState("starting")
	params := map[string]any{"cwd": u.cwd, "approvalsReviewer": u.approvalsReviewer()}
	method := "thread/start"
	if id != "" {
		method = "thread/resume"
		params["threadId"] = id
	}
	return u.request(method, params)
}

func (u *chatUI) initializeChatServer() error {
	if u.connected {
		return u.completeChatInitialization(false)
	}
	return u.request("initialize", chatInitializeParams())
}

func (u *chatUI) completeChatInitialization(sendNotification bool) error {
	u.connected = true
	if sendNotification {
		if err := u.c.encoder.Encode(map[string]string{"method": "initialized"}); err != nil {
			return err
		}
	}
	if err := u.requestDefaultModels(); err != nil {
		return err
	}
	if err := u.requestCustomCommands(); err != nil {
		return err
	}
	if u.resumeAtStart && u.thread == "" {
		u.busy = false
		u.setAgentState("idle")
		u.sessionPickerRequested = chatUIIsInteractive(u) && !u.resumeLatest
		return u.startChatSessionSearch("")
	}
	return u.open(u.thread)
}

func (u *chatUI) interrupt() error {
	for _, p := range u.pending {
		if p.method == "initialize" || p.method == "thread/start" || p.method == "thread/resume" {
			u.print("Conversation is still starting. Wait for it to open before cancelling a turn.\n")
			return nil
		}
	}
	if err := u.cancelQuestionRequests("Question request cancelled because the turn was interrupted."); err != nil {
		return err
	}
	u.cancel = true
	u.setActivityLabel("Stopping")
	for _, m := range u.approvals {
		if err := u.c.reply(m.ID, map[string]string{"decision": "cancel"}); err != nil {
			return err
		}
	}
	u.approvals = nil
	if u.turn != "" {
		u.cancel = false
		u.setAgentState("running")
		return u.request("turn/interrupt", map[string]string{"threadId": u.thread, "turnId": u.turn})
	}
	if u.busy {
		u.setAgentState("running")
	} else {
		u.setAgentState("idle")
	}
	return nil
}
func (u *chatUI) response(m appMessage) error {
	p, ok := u.pending[string(m.ID)]
	if !ok {
		return nil
	}
	delete(u.pending, string(m.ID))
	if u.acceptChatAgentResponse(m, p) {
		return nil
	}
	if (chatGoalRequest(p.method) || p.method == "thread/settings/update") && p.threadID != "" && p.threadID != u.thread {
		return nil
	}
	if p.method == "thread/settings/update" {
		return u.acceptChatPlanResponse(m, p)
	}
	if p.method == "jara/btw" {
		u.acceptChatBtwResponse(m)
		return nil
	}
	if u.acceptChatExtraResponse(m, p) {
		return nil
	}
	if chatDefaultRequest(p.method) {
		u.acceptChatDefaultResponse(m, p)
		return nil
	}
	if m.Error != nil {
		if p.method == "jara/shell" {
			u.printRole("system", "Shell command failed: "+chatText(m.Error.Message)+"\n")
			return nil
		}
		if p.method == "initialize" && chatAlreadyInitialized(m.Error.Message) {
			return u.completeChatInitialization(false)
		}
		if p.method == "thread/list" {
			err := fmt.Errorf("Codex app-server rejected thread/list: %s", chatText(m.Error.Message))
			if !u.failChatSessionRequest(err) {
				u.print("\nSaved session lookup failed: " + chatText(m.Error.Message) + "\n")
				if !u.busy {
					u.print("You> ")
				}
			}
			return nil
		}
		if p.method == "model/list" {
			err := fmt.Errorf("Codex app-server rejected model/list: %s", chatText(m.Error.Message))
			if !u.failChatModelRequest(err) {
				u.print("\nModel lookup failed: " + chatText(m.Error.Message) + "\n")
				if !u.busy {
					u.print("You> ")
				}
			}
			return nil
		}
		if chatGoalRequest(p.method) {
			u.print("\n" + chatGoalRequestError(m.Error.Message) + "\n" + u.chatGoalPrompt())
			return nil
		}
		if chatScheduleRequest(p.method) {
			u.print("\nScheduled prompt request failed: " + chatText(m.Error.Message) + "\n" + u.schedulePrompt())
			return nil
		}
		if chatCrunchRequest(p.method) {
			u.print("\nCrunch request failed: " + chatText(m.Error.Message) + "\n" + u.schedulePrompt())
			return nil
		}
		if p.method == "turn/start" {
			if p.queuedPrompt != "" {
				u.queuedPrompts = append([]chatQueuedPrompt{u.newQueuedPrompt(p.threadID, p.queuedPrompt)}, u.queuedPrompts...)
			}
			request, tracked := u.pendingTurnModels[string(m.ID)]
			delete(u.pendingTurnModels, string(m.ID))
			if tracked && request.thread != u.thread {
				return nil
			}
		}
		if recoverChatRequestError(u, p, m) {
			return nil
		}
		return fmt.Errorf("%s: %s", p.method, chatText(m.Error.Message))
	}
	switch p.method {
	case "initialize":
		return u.completeChatInitialization(true)
	case "jara/shell":
		return u.acceptChatShellResponse(m)
	case "thread/start", "thread/resume":
		var r chatThreadResponse
		if err := json.Unmarshal(m.Result, &r); err != nil {
			return err
		}
		if r.Thread.ID == "" {
			return fmt.Errorf("app-server returned an empty thread ID")
		}
		previousThread := u.thread
		u.thread = r.Thread.ID
		if previousThread != "" && previousThread != u.thread {
			u.chatConversationState = chatConversationState{}
		}
		u.contextUsage = chatContextUsage{}
		u.setEffectiveModel(r.Model)
		u.resolvedModel = chatText(r.ResolvedModel)
		u.effectiveReasoning = ""
		if r.ReasoningEffort != nil {
			u.effectiveReasoning = *r.ReasoningEffort
		} else if r.Thread.ReasoningEffort != nil {
			u.effectiveReasoning = *r.Thread.ReasoningEffort
		}
		u.threadReady = true
		u.busy = p.method == "thread/resume" && u.attachedTurnActive
		if u.busy {
			u.setActivityLabel("Thinking")
			u.setAgentState("running")
		} else {
			u.setAgentState("idle")
		}
		// Explicit metadata links this terminal to its saved conversation.
		if u.trackAgentState && os.Getenv("TMUX") != "" {
			_ = tmux("set-option", "-t", os.Getenv("TMUX_PANE"), "@jara_codex_thread", u.thread).Run()
		}
		if !chatUIIsInteractive(u) {
			u.print("\nConversation " + u.thread + "\n")
		}
		u.renderChatHistory(r.Thread.Turns)
		u.refreshGitDiff()
		if u.afterHistory != "" {
			u.printRole("system", u.afterHistory)
			u.afterHistory = ""
		}
		started, err := u.dispatchQueuedPrompt()
		if err != nil {
			return err
		}
		if !started {
			u.print("\nYou> ")
		}
	case "thread/list":
		return u.acceptChatSessionPage(m.Result)
	case "model/list":
		return u.acceptChatModelPage(m.Result)
	case "thread/goal/get", "thread/goal/set", "thread/goal/clear":
		return u.acceptChatGoalResponse(m, p.method, p.action)
	case "jara/schedule/create", "jara/schedule/list", "jara/schedule/cancel":
		return u.acceptChatScheduleResponse(m, p.action)
	case "jara/crunch/create", "jara/crunch/status", "jara/crunch/stop":
		return u.acceptChatCrunchResponse(m, p.action)
	case "turn/start":
		var r struct{ Turn struct{ ID string } }
		if err := json.Unmarshal(m.Result, &r); err != nil {
			return err
		}
		if request, ok := u.pendingTurnModels[string(m.ID)]; ok {
			delete(u.pendingTurnModels, string(m.ID))
			if request.thread != u.thread {
				return nil
			}
			if request.model != "" {
				u.setEffectiveModel(request.model)
				if request.effort == "" {
					u.effectiveReasoning = ""
					if model := u.modelByID(request.model); model != nil {
						u.effectiveReasoning = model.DefaultReasoningEffort
					}
				}
				if request.modelGeneration == u.modelSelectionGeneration {
					u.nextModel = ""
					u.nextModelLabel = ""
				}
			}
			if request.effort != "" {
				u.effectiveReasoning = request.effort
				if request.effortGeneration == u.reasoningSelectionGeneration {
					u.nextReasoning = ""
				}
			}
		}
		if u.busy {
			u.turn = r.Turn.ID
			u.notePromptTurn(r.Turn.ID)
			u.turnStartedAt = time.Now()
			u.setAgentState(u.currentState())
			if u.cancel {
				return u.interrupt()
			}
		}
	}
	return nil
}
