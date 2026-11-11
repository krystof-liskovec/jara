package jara

import (
	"fmt"
	"strconv"
	"strings"
)

// /rewind goes back to before an earlier prompt: the agent forgets that
// prompt and everything after it, the transcript reloads, and the prompt is
// put back in the input box to edit and send again. Files are not reverted.

type chatPromptEntry struct {
	turnID string // the turn the prompt started, once known
	text   string
}

// notePromptTurn links the newest prompt to the turn it started.
func (u *chatUI) notePromptTurn(turnID string) {
	if count := len(u.promptLog); turnID != "" && count > 0 && u.promptLog[count-1].turnID == "" {
		u.promptLog[count-1].turnID = turnID
	}
}

func (u *chatUI) runChatRewindCommand(args string) error {
	switch {
	case !u.threadReady:
		u.print("No conversation is open. Use /resume ID or /new first.\n")
		return nil
	case u.busy:
		u.print("A turn is running. Use /cancel or wait for it to finish before rewinding.\n")
		return nil
	case len(u.promptLog) == 0:
		u.print("There is nothing to rewind yet.\n")
		return nil
	}
	if args == "" {
		if chatUIIsInteractive(u) {
			u.pickerRequested = "rewind"
			return nil
		}
		var b strings.Builder
		b.WriteString("Prompts in this conversation:\n")
		for index, entry := range u.promptLog {
			fmt.Fprintf(&b, "%d. %s\n", index+1, compactChatSessionText(entry.text, 80))
		}
		b.WriteString("Use /rewind N to go back to before prompt N.\n")
		u.print(b.String())
		return nil
	}
	choice, err := strconv.Atoi(args)
	if err != nil || choice < 1 || choice > len(u.promptLog) {
		u.print(fmt.Sprintf("Choose a prompt from 1 to %d.\n", len(u.promptLog)))
		return nil
	}
	return u.rewindTo(choice - 1)
}

func (u *chatUI) rewindTo(index int) error {
	if index < 0 || index >= len(u.promptLog) || u.busy {
		return nil
	}
	entry := u.promptLog[index]
	params := map[string]any{"threadId": u.thread, "turnId": entry.turnID, "drop": len(u.promptLog) - index}
	return u.requestWithAction("jara/rewind", params, entry.text)
}

func (u *chatUI) acceptChatRewindResponse(m appMessage, p chatPending) bool {
	if m.Error != nil {
		u.printRole("system", "Could not rewind: "+chatText(m.Error.Message)+"\n")
		return true
	}
	u.clearChatTranscript()
	u.prefill = p.action
	u.afterHistory = "Rewound to before this prompt; it is back in the input box to edit and send. Files were not reverted.\n"
	if err := u.open(u.thread); err != nil {
		u.printRole("system", "Could not reload the conversation: "+err.Error()+"\n")
	}
	return true
}

func chatRewindPickerChoices(u *chatUI, selected int) []chatCompletionChoice {
	choices := []chatCompletionChoice{{Value: "Rewind to before a prompt", Description: "Esc cancels · files are not reverted"}}
	for index, entry := range u.promptLog {
		choices = append(choices, chatCompletionChoice{
			Value:       strconv.Itoa(index+1) + ". " + compactChatSessionText(entry.text, chatSessionTitleRunes),
			Description: "drops this prompt and everything after it",
			Selected:    index == selected,
			Index:       index,
			Total:       len(u.promptLog),
		})
	}
	return choices
}
