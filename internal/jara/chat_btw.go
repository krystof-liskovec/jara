package jara

import (
	"encoding/json"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/agentrouter"
)

// /btw asks a side question about the conversation without adding it to the
// conversation. It works while a turn is running; the agent router answers in
// a fork that is discarded afterwards.
func isChatBtwCommand(line string) bool {
	line = strings.TrimSpace(line)
	return line == "/btw" || strings.HasPrefix(line, "/btw ") || strings.HasPrefix(line, "/btw\t")
}

func (u *chatUI) runChatBtwCommand(line string) error {
	question := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/btw"))
	switch {
	case question == "":
		u.print("Use /btw QUESTION to ask a side question without adding it to the conversation.\n")
		return nil
	case len(question) > maxBackgroundPrompt:
		u.print("Side question exceeds 64 KiB.\n")
		return nil
	case !u.threadReady:
		u.print("No conversation is open. Use /resume ID or /new first.\n")
		return nil
	}
	u.printRole("btw", "› "+question+"\n")
	id, err := u.c.request("jara/btw", map[string]string{"threadId": u.thread, "question": question})
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{method: "jara/btw", deadline: time.Now().Add(agentrouter.BtwTimeout + time.Minute), threadID: u.thread}
	return nil
}

func (u *chatUI) acceptChatBtwResponse(m appMessage) {
	if m.Error != nil {
		u.printRole("btw", "Could not answer the side question: "+chatText(m.Error.Message)+"\n")
		return
	}
	var result struct {
		Answer string `json:"answer"`
	}
	_ = json.Unmarshal(m.Result, &result)
	answer := strings.TrimSpace(result.Answer)
	if answer == "" {
		answer = "(no answer)"
	}
	u.printRole("btw", answer+"\n")
}
