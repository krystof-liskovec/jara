package jara

import "strings"

func (u *chatUI) line(line string) (bool, error) {
	if u.composing {
		trimmed := strings.TrimSpace(line)
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
			return true, nil
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
		if err := u.cancelQuestionRequests("User exited the conversation."); err != nil {
			return false, err
		}
		return true, nil
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
		u.setAgentState(u.currentState())
		return false, u.c.reply(m.ID, map[string]string{"decision": decision})
	case line == "/status":
		u.print("\n" + u.status() + "You> ")
		return false, nil
	case line == "/help":
		u.print("\n" + u.help() + "You> ")
		return false, nil
	case line == "/sessions" || strings.HasPrefix(line, "/sessions ") || strings.HasPrefix(line, "/sessions\t"):
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
		if err := u.startChatModelList(); err != nil {
			u.print("\n" + err.Error() + "\n")
			if !u.busy {
				u.print("You> ")
			}
			return false, nil
		}
		u.print("Loading available Codex models...\n")
		return false, nil
	case strings.HasPrefix(line, "/models "):
		u.print("Use /models to list choices, then /model ID or /model N to select one.\nYou> ")
		return false, nil
	case line == "/model":
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
	case line == "/resume":
		u.print("Use /resume N to open a listed session or /resume ID for a conversation ID.\nYou> ")
		return false, nil
	case line == "/new":
		return false, u.open("")
	case line == "":
		u.print("You> ")
		return false, nil
	default:
		return false, u.sendPrompt(line)
	}
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
	if !u.threadReady {
		u.print("No conversation is open. Use /resume ID or /new first.\nYou> ")
		return nil
	}
	u.busy = true
	u.cancel = false
	u.setAgentState("running")
	params := map[string]any{"threadId": u.thread, "input": []any{map[string]any{"type": "text", "text": message}}}
	if u.nextModel != "" {
		params["model"] = u.nextModel
	}
	return u.requestTurn(params)
}
