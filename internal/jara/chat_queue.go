package jara

import (
	"fmt"
	"strconv"
	"strings"
)

const chatPromptQueueLimit = 32

type chatQueuedPrompt struct {
	id       uint64
	threadID string
	text     string
}

func (u *chatUI) queuePrompt(threadID, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if threadID == "" || prompt == "" {
		return fmt.Errorf("enter a prompt before queuing it")
	}
	if len(prompt) > maxBackgroundPrompt {
		return fmt.Errorf("queued prompt exceeds 64 KiB")
	}
	if len(u.queuedPrompts) >= chatPromptQueueLimit {
		return fmt.Errorf("prompt queue is full (%d prompts)", chatPromptQueueLimit)
	}
	u.queuedPrompts = append(u.queuedPrompts, u.newQueuedPrompt(threadID, prompt))
	return nil
}

func (u *chatUI) newQueuedPrompt(threadID, text string) chatQueuedPrompt {
	u.queuePromptSequence++
	if u.queuePromptSequence == 0 {
		u.queuePromptSequence++
	}
	return chatQueuedPrompt{id: u.queuePromptSequence, threadID: threadID, text: text}
}

func isChatQueueCommand(line string) bool {
	return line == "/queue" || strings.HasPrefix(line, "/queue ") || strings.HasPrefix(line, "/queue\t")
}

func (u *chatUI) runChatQueueCommand(line string) error {
	args := strings.TrimSpace(strings.TrimPrefix(line, "/queue"))
	if args == "run" {
		started, err := u.dispatchQueuedPrompt()
		if err != nil {
			return err
		}
		if !started {
			u.print("No queued prompt is ready for this conversation.\n" + u.schedulePrompt())
		}
		return nil
	}
	if args == "clear" {
		remaining := u.queuedPrompts[:0]
		cleared := 0
		for _, prompt := range u.queuedPrompts {
			if prompt.threadID == u.thread {
				cleared++
				continue
			}
			remaining = append(remaining, prompt)
		}
		u.queuedPrompts = remaining
		u.print(fmt.Sprintf("Cleared %d queued prompt(s) for this conversation.\n%s", cleared, u.schedulePrompt()))
		return nil
	}
	if strings.HasPrefix(args, "edit") {
		parts := strings.Fields(args)
		if len(parts) != 2 || parts[0] != "edit" {
			u.print("Usage: /queue | /queue run | /queue clear | /queue edit N\n" + u.schedulePrompt())
			return nil
		}
		if !chatUIIsInteractive(u) {
			u.print("Editing queued prompts requires the interactive chat interface.\n" + u.schedulePrompt())
			return nil
		}
		number, err := strconv.Atoi(parts[1])
		if err != nil || number < 1 {
			u.print("Choose a queued prompt number from /queue.\n" + u.schedulePrompt())
			return nil
		}
		_, prompt, ok := u.queuedPromptAt(number)
		if !ok {
			u.print("That queued prompt does not exist. Use /queue to see the current queue.\n" + u.schedulePrompt())
			return nil
		}
		u.queueEditRequest = prompt.id
		return nil
	}
	if args != "" {
		u.print("Usage: /queue | /queue run | /queue clear | /queue edit N\n" + u.schedulePrompt())
		return nil
	}
	var b strings.Builder
	index := 0
	for _, prompt := range u.queuedPrompts {
		if prompt.threadID != u.thread {
			continue
		}
		index++
		fmt.Fprintf(&b, "%d. %s\n", index, compactScheduledPrompt(prompt.text))
	}
	if index == 0 {
		b.WriteString("No queued prompts for this conversation.\n")
	}
	u.print(b.String() + u.schedulePrompt())
	return nil
}

func (u *chatUI) queuedPromptAt(number int) (int, chatQueuedPrompt, bool) {
	if number < 1 {
		return -1, chatQueuedPrompt{}, false
	}
	current := 0
	for index, prompt := range u.queuedPrompts {
		if prompt.threadID != u.thread {
			continue
		}
		current++
		if current == number {
			return index, prompt, true
		}
	}
	return -1, chatQueuedPrompt{}, false
}

func (u *chatUI) queuedPromptByID(id uint64) (int, int, chatQueuedPrompt, bool) {
	if id == 0 {
		return -1, 0, chatQueuedPrompt{}, false
	}
	current := 0
	for index, prompt := range u.queuedPrompts {
		if prompt.threadID != u.thread {
			continue
		}
		current++
		if prompt.id == id {
			return index, current, prompt, true
		}
	}
	return -1, 0, chatQueuedPrompt{}, false
}

func (u *chatUI) queueViewLines(editingID uint64) []chatViewLine {
	var lines []chatViewLine
	number := 0
	for _, prompt := range u.queuedPrompts {
		if prompt.threadID != u.thread {
			continue
		}
		number++
		kind := "queued-prompt"
		marker := "  "
		if prompt.id == editingID {
			kind, marker = "queued-prompt-edit", "› "
		}
		lines = append(lines, chatViewLine{
			text: fmt.Sprintf("%sQueued %d · %s", marker, number, compactScheduledPrompt(prompt.text)),
			kind: kind,
		})
	}
	return lines
}

func (u *chatUI) dispatchQueuedPrompt() (bool, error) {
	if !u.threadReady || u.agentTurnActive() || u.chatPlanUpdatePending() {
		return false, nil
	}
	for index, prompt := range u.queuedPrompts {
		if prompt.threadID != u.thread {
			continue
		}
		u.queuedPrompts = append(u.queuedPrompts[:index], u.queuedPrompts[index+1:]...)
		u.printRole("system", fmt.Sprintf("\nRunning queued prompt (%d remaining).\n", len(u.queuedPrompts)))
		if err := u.sendQueuedPrompt(prompt.text); err != nil {
			u.queuedPrompts = append(u.queuedPrompts, chatQueuedPrompt{})
			copy(u.queuedPrompts[index+1:], u.queuedPrompts[index:])
			u.queuedPrompts[index] = prompt
			return true, err
		}
		return true, nil
	}
	return false, nil
}
