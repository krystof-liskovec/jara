package jara

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const chatPromptQueueLimit = 32

type chatQueuedPrompt struct {
	id       uint64
	threadID string
	text     string
	images   []string // data URLs of the [Image #N] the prompt mentioned
	session  string   // the persistent session's ID for this prompt
}

// queuePrompt queues a prompt from the composer with the attached images it
// mentions.
func (u *chatUI) queuePrompt(threadID, prompt string) error {
	if err := u.checkQueuedPrompt(threadID, prompt); err != nil {
		return err
	}
	return u.queuePromptIn(threadID, prompt, u.takeChatImageURLs(prompt), false)
}

// queueLocalPrompt queues a prompt this window sends itself, after a change
// it is waiting for.
func (u *chatUI) queueLocalPrompt(threadID, prompt string, images []string) error {
	return u.queuePromptIn(threadID, prompt, images, true)
}

func (u *chatUI) checkQueuedPrompt(threadID, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if threadID == "" || prompt == "" {
		return fmt.Errorf("enter a prompt before queuing it")
	}
	if len(prompt) > maxBackgroundPrompt {
		return fmt.Errorf("queued prompt exceeds 64 KiB")
	}
	return nil
}

// queuePromptIn queues prompt with its images, already taken from the
// composer.
func (u *chatUI) queuePromptIn(threadID, prompt string, images []string, local bool) error {
	if err := u.checkQueuedPrompt(threadID, prompt); err != nil {
		return err
	}
	prompt = strings.TrimSpace(prompt)
	if u.persistentSession && !u.chatOffline() && !local {
		// The session keeps the queue, so it survives this window and moves
		// with the conversation; jara/queue/updated shows the result. While
		// reconnecting, the window keeps the prompt and sends it afterwards.
		// The images go along: the session starts the turn without this window.
		params := map[string]any{"threadId": threadID, "prompt": prompt}
		if len(images) > 0 {
			params["images"] = images
		}
		return u.request("jara/queue/add", params)
	}
	if len(u.queuedPrompts) >= chatPromptQueueLimit {
		return fmt.Errorf("prompt queue is full (%d prompts)", chatPromptQueueLimit)
	}
	entry := u.newQueuedPrompt(threadID, prompt)
	entry.images = images
	u.queuedPrompts = append(u.queuedPrompts, entry)
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
	if args == "run" && u.persistentSession {
		u.print("Queued prompts start by themselves whenever the conversation is idle.\n" + u.schedulePrompt())
		return nil
	}
	if args == "clear" && u.persistentSession {
		u.print("Clearing the queued prompts of this conversation.\n" + u.schedulePrompt())
		return u.request("jara/queue/clear", map[string]string{"threadId": u.thread})
	}
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

// queueViewLines shows this conversation's queue above the composer: a
// dimmed header with the count, then one numbered row per prompt in the order
// they run (› marks the one being edited).
func (u *chatUI) queueViewLines(editingID uint64) []chatViewLine {
	var lines []chatViewLine
	number := 0
	for _, prompt := range u.queuedPrompts {
		if prompt.threadID != u.thread {
			continue
		}
		number++
		kind, marker := "queued-prompt", " "
		if prompt.id == editingID {
			kind, marker = "queued-prompt-edit", "›"
		}
		lines = append(lines, chatViewLine{
			text: fmt.Sprintf(" %s%2d  %s", marker, number, compactScheduledPrompt(prompt.text)),
			kind: kind,
		})
	}
	if number == 0 {
		return nil
	}
	header := chatViewLine{text: fmt.Sprintf("Queued · %d · start when idle · /queue edit N", number), kind: "queued-prompt-header"}
	return append([]chatViewLine{header}, lines...)
}

func (u *chatUI) dispatchQueuedPrompt() (bool, error) {
	if !u.threadReady || u.agentTurnActive() || u.chatPlanUpdatePending() {
		return false, nil
	}
	for index, prompt := range u.queuedPrompts {
		if prompt.threadID != u.thread || prompt.session != "" { // the session runs its own queue
			continue
		}
		u.queuedPrompts = append(u.queuedPrompts[:index], u.queuedPrompts[index+1:]...)
		if err := u.sendQueuedPrompt(prompt.text, prompt.images); err != nil {
			u.queuedPrompts = append(u.queuedPrompts, chatQueuedPrompt{})
			copy(u.queuedPrompts[index+1:], u.queuedPrompts[index:])
			u.queuedPrompts[index] = prompt
			return true, err
		}
		return true, nil
	}
	return false, nil
}

// acceptQueueUpdate mirrors the session's queue. Prompts keep their local
// numbers so an edit in progress stays attached to its prompt.
func (u *chatUI) acceptQueueUpdate(params json.RawMessage) {
	var update struct {
		Prompts []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
			Prompt   string `json:"prompt"`
		} `json:"prompts"`
	}
	if json.Unmarshal(params, &update) != nil {
		return
	}
	known := map[string]uint64{}
	for _, prompt := range u.queuedPrompts {
		if prompt.session != "" {
			known[prompt.session] = prompt.id
		}
	}
	queue := make([]chatQueuedPrompt, 0, len(update.Prompts)+len(u.queuedPrompts))
	for _, prompt := range update.Prompts {
		entry := u.newQueuedPrompt(prompt.ThreadID, chatText(prompt.Prompt))
		if id, ok := known[prompt.ID]; ok {
			entry.id = id
		}
		entry.session = prompt.ID
		queue = append(queue, entry)
	}
	for _, prompt := range u.queuedPrompts {
		if prompt.session == "" { // typed while reconnecting; sent by this window
			queue = append(queue, prompt)
		}
	}
	u.queuedPrompts = queue
}
