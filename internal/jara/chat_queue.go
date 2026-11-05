package jara

import (
	"fmt"
	"strings"
)

const chatPromptQueueLimit = 32

type chatQueuedPrompt struct {
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
	u.queuedPrompts = append(u.queuedPrompts, chatQueuedPrompt{threadID: threadID, text: prompt})
	return nil
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
	if args != "" {
		u.print("Usage: /queue | /queue run | /queue clear\n" + u.schedulePrompt())
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
