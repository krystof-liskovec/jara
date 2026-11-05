package jara

import (
	"strings"
	"time"
)

func (u *chatUI) line(line string) (bool, error) {
	if u.composing {
		trimmed := strings.TrimSpace(line)
		if isChatGoalCommand(trimmed) {
			return false, u.runChatGoalCommand(trimmed)
		}
		if isChatScheduleCommand(trimmed) {
			return false, u.runChatScheduleCommand(trimmed)
		}
		if isChatCrunchCommand(trimmed) {
			return false, u.runChatCrunchCommand(trimmed, false)
		}
		if isChatGoalCrunchCommand(trimmed) {
			return false, u.runChatCrunchCommand(trimmed, true)
		}
		if isChatQueueCommand(trimmed) {
			return false, u.runChatQueueCommand(trimmed)
		}
		switch trimmed {
		case "/send":
			return false, u.sendDraft()
		case "/discard":
			u.composing = false
			u.draft = nil
			u.draftBytes = 0
			u.print("Draft discarded.\nYou> ")
			return false, nil
		case "/help":
			u.print("\n" + u.help() + "Compose> ")
			return false, nil
		case "/status":
			u.print("\n" + u.status() + "Compose> ")
			return false, nil
		case "/quit":
			return true, u.quitSession()
		case "/detach":
			if !u.persistentSession {
				u.print("This conversation cannot be detached from its current session.\nCompose> ")
				return false, nil
			}
			return true, u.sendPersistentExit("jara/detach")
		case "/stop":
			if err := u.cancelQuestionRequests("Question request cancelled because the session was stopped."); err != nil {
				return false, err
			}
			return true, u.sendPersistentExit("jara/stop")
		default:
			if strings.HasPrefix(trimmed, "//") {
				leading := len(line) - len(strings.TrimLeft(line, " \t"))
				line = line[:leading] + line[leading+1:]
			} else if strings.HasPrefix(trimmed, "/") {
				u.print("Unknown command: " + trimmed + ". Use /help to see commands. Prefix a literal slash line with //.\nCompose> ")
				return false, nil
			}
			if u.draftBytes+len(line)+1 > maxBackgroundPrompt {
				u.print("Draft reached the input size limit. Enter /send or /discard.\nCompose> ")
				return false, nil
			}
			u.draft = append(u.draft, line)
			u.draftBytes += len(line) + 1
			u.print("Compose> ")
			return false, nil
		}
	}
	line = strings.TrimSpace(line)
	switch {
	case line == "/quit":
		return true, u.quitSession()
	case line == "/stop":
		if err := u.cancelQuestionRequests("Question request cancelled because the session was stopped."); err != nil {
			return false, err
		}
		return true, u.sendPersistentExit("jara/stop")
	case line == "/detach":
		if !u.persistentSession {
			u.print("This conversation cannot be detached from its current session.\nYou> ")
			return false, nil
		}
		return true, u.sendPersistentExit("jara/detach")
	case line == "/cancel":
		return false, u.interrupt()
	case line == "/approve" || line == "/deny":
		if len(u.approvals) == 0 {
			u.print("No approval is pending.\n")
			return false, nil
		}
		decision := "decline"
		if line == "/approve" {
			decision = "accept"
		}
		m := u.approvals[0]
		u.approvals = u.approvals[1:]
		if len(u.approvals) > 0 {
			u.activityLabel = "Waiting for approval"
		} else if len(u.questionRequests) > 0 {
			u.activityLabel = "Waiting for input"
		} else if u.busy {
			u.activityLabel = "Thinking"
		}
		u.setAgentState(u.currentState())
		return false, u.c.reply(m.ID, map[string]string{"decision": decision})
	case line == "/status":
		u.print("\n" + u.status() + "You> ")
		return false, nil
	case isChatGoalCommand(line):
		return false, u.runChatGoalCommand(line)
	case isChatScheduleCommand(line):
		return false, u.runChatScheduleCommand(line)
	case isChatCrunchCommand(line):
		return false, u.runChatCrunchCommand(line, false)
	case isChatGoalCrunchCommand(line):
		return false, u.runChatCrunchCommand(line, true)
	case isChatQueueCommand(line):
		return false, u.runChatQueueCommand(line)
	case line == "/help":
		u.print("\n" + u.help() + "You> ")
		return false, nil
	case line == "/resume":
		u.sessionPickerRequested = true
		if err := u.startChatSessionSearch(""); err != nil {
			u.sessionPickerRequested = false
			u.print("\n" + err.Error() + "\n")
			if !u.busy {
				u.print("You> ")
			}
			return false, nil
		}
		if !chatUIIsInteractive(u) {
			u.print("Loading recent sessions, newest first...\n")
		}
		return false, nil
	case line == "/sessions" || strings.HasPrefix(line, "/sessions ") || strings.HasPrefix(line, "/sessions\t"):
		u.sessionPickerRequested = false
		query := strings.TrimSpace(line[len("/sessions"):])
		if err := u.startChatSessionSearch(query); err != nil {
			u.print("\n" + err.Error() + "\n")
			if !u.busy {
				u.print("You> ")
			}
			return false, nil
		}
		u.print("Searching saved sessions in this folder...\n")
		return false, nil
	case line == "/models":
		if chatUIIsInteractive(u) {
			u.pickerRequested = "model"
		}
		if err := u.startChatModelList(); err != nil {
			u.pickerRequested = ""
			u.print("\n" + err.Error() + "\n")
			if !u.busy {
				u.print("You> ")
			}
			return false, nil
		}
		if !chatUIIsInteractive(u) {
			u.print("Loading available Codex models...\n")
		}
		return false, nil
	case strings.HasPrefix(line, "/models "):
		u.print("Use /models or /model to open the model picker; /model ID also selects directly.\nYou> ")
		return false, nil
	case line == "/model":
		if chatUIIsInteractive(u) {
			u.pickerRequested = "model"
			if u.models != nil && u.models.complete && u.models.failure == "" {
				return false, nil
			}
			if err := u.startChatModelList(); err != nil {
				u.pickerRequested = ""
				u.print("\n" + err.Error() + "\n")
			}
			return false, nil
		}
		u.print("Use /model ID or /model N to choose a model for this conversation.\nYou> ")
		return false, nil
	case strings.HasPrefix(line, "/model "):
		choice := strings.TrimSpace(strings.TrimPrefix(line, "/model "))
		if err := u.chooseChatModel(choice); err != nil {
			u.print("\n" + err.Error() + "\nYou> ")
			return false, nil
		}
		u.print("Model set to " + u.nextModelLabel + " for the next and subsequent turns in this conversation.\nYou> ")
		return false, nil
	case line == "/reasoning":
		if chatUIIsInteractive(u) {
			u.pickerRequested = "reasoning"
			if u.models != nil && u.models.complete && u.models.failure == "" {
				return false, nil
			}
			if err := u.startChatModelList(); err != nil {
				u.pickerRequested = ""
				u.print("\n" + err.Error() + "\n")
			}
			return false, nil
		}
		u.print("Use /reasoning EFFORT to select a reasoning level.\nYou> ")
		return false, nil
	case strings.HasPrefix(line, "/reasoning "):
		if err := u.chooseChatReasoning(strings.TrimSpace(strings.TrimPrefix(line, "/reasoning "))); err != nil {
			u.print("\n" + err.Error() + "\nYou> ")
			return false, nil
		}
		u.print("Reasoning set to " + chatReasoningEffortLabel(u.nextReasoning) + " for the next and subsequent turns.\nYou> ")
		return false, nil
	case line == "/answer" || strings.HasPrefix(line, "/answer "):
		return false, u.answerQuestion(line)
	case line == "/compose":
		if !u.threadReady {
			u.print("No conversation is open. Use /resume ID or /new first.\nYou> ")
			return false, nil
		}
		if u.busy {
			u.print("A turn is running. Use /cancel or wait for it to finish.\n")
			return false, nil
		}
		u.composing = true
		u.draft = nil
		u.draftBytes = 0
		u.print("Compose your message across multiple lines. Enter /send to send, /discard to clear it, or // to start a literal slash line.\nCompose> ")
		return false, nil
	case line == "/send" || line == "/discard":
		u.print("No draft is being composed. Use /compose first.\n")
		return false, nil
	case strings.HasPrefix(line, "/") && line != "/new" && line != "/resume" && !strings.HasPrefix(line, "/resume "):
		u.print("Unknown command: " + line + ". Use /help to see commands.\n")
		return false, nil
	case u.busy:
		u.print("A turn is running. Use /cancel or wait for it to finish.\n")
		return false, nil
	case strings.HasPrefix(line, "/resume "):
		target := strings.TrimSpace(strings.TrimPrefix(line, "/resume "))
		if target == "" {
			u.print("Use /resume N to open a listed session or /resume ID for a conversation ID.\nYou> ")
			return false, nil
		}
		if numericChatSessionChoice(target) {
			if err := u.resumeChatSessionChoice(target); err != nil {
				u.print("\n" + err.Error() + "\nYou> ")
				return false, nil
			}
			return false, nil
		}
		return false, u.open(target)
	case line == "/new":
		return false, u.open("")
	case line == "":
		u.print("You> ")
		return false, nil
	default:
		return false, u.sendPrompt(line)
	}
}

func (u *chatUI) sendPersistentExit(method string) error {
	if !u.persistentSession {
		return nil
	}
	return u.c.encoder.Encode(map[string]string{"method": method})
}

func (u *chatUI) quitSession() error {
	if u.persistentSession {
		method := "jara/close"
		if u.agentTurnActive() {
			method = "jara/detach"
		}
		return u.sendPersistentExit(method)
	}
	return u.cancelQuestionRequests("User exited the conversation.")
}

func (u *chatUI) stopIdleSession() error {
	if !u.persistentSession || u.agentTurnActive() {
		return nil
	}
	return u.sendPersistentExit("jara/close")
}

func (u *chatUI) agentTurnActive() bool {
	if u.attachedTurnActive || u.turn != "" {
		return true
	}
	for _, pending := range u.pending {
		if pending.method == "turn/start" {
			return true
		}
	}
	return u.threadReady && u.busy
}

func (u *chatUI) sendDraft() error {
	if !u.threadReady {
		u.print("No conversation is open. Resume a session or start a new one first. Your draft is still here.\nCompose> ")
		return nil
	}
	message := strings.Join(u.draft, "\n")
	if strings.TrimSpace(message) == "" {
		u.print("Draft is empty. Add text or use /discard.\n")
		return nil
	}
	u.composing = false
	u.draft = nil
	u.draftBytes = 0
	u.print("Sending message.\n")
	return u.sendPrompt(message)
}

func (u *chatUI) sendPrompt(message string) error {
	return u.sendPromptWithQueue(message, "")
}

func (u *chatUI) sendQueuedPrompt(message string) error {
	return u.sendPromptWithQueue(message, message)
}

func (u *chatUI) sendPromptWithQueue(message, queuedPrompt string) error {
	if !u.threadReady {
		u.print("No conversation is open. Use /resume ID or /new first.\nYou> ")
		return nil
	}
	u.recordRole("user", message)
	u.busy = true
	u.cancel = false
	u.turnStartedAt = time.Now()
	u.activityLabel = "Thinking"
	u.setAgentState("running")
	params := map[string]any{
		"threadId": u.thread,
		"input":    []any{map[string]any{"type": "text", "text": message}},
		"summary":  "concise",
	}
	if u.nextModel != "" {
		params["model"] = u.nextModel
	}
	if u.nextReasoning != "" {
		params["effort"] = u.nextReasoning
	}
	return u.requestTurnWithQueuedPrompt(params, queuedPrompt)
}
