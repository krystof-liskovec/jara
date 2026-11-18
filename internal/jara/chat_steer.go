package jara

import (
	"strings"
	"time"
)

// A prompt sent while the agent works steers the running turn with
// turn/steer: the model reads it at its next step, without ending the turn.
// The agent router does the same for Claude Code by writing the message to
// its input. When the turn cannot take it, the prompt waits in the queue and
// runs right after the turn instead: review and compaction turns, a turn that
// is just ending, or a turn not yet started.

// steerPrompt hands message to the running turn, or queues it.
func (u *chatUI) steerPrompt(message string) error {
	if !u.canSteer() {
		return u.queuePrompt(u.thread, message)
	}
	params := map[string]any{
		"threadId":       u.thread,
		"expectedTurnId": u.turn,
		"input":          append([]any{map[string]any{"type": "text", "text": message}}, u.takeChatImages(message)...),
	}
	id, err := u.c.request("turn/steer", params)
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{method: "turn/steer", deadline: time.Now().Add(30 * time.Second), threadID: u.thread, queuedPrompt: message}
	u.recordRole("user", message)
	return nil
}

// canSteer reports whether the running turn can take a message now.
func (u *chatUI) canSteer() bool {
	return u.threadReady && u.agentTurnActive() && u.turn != ""
}

// acceptChatSteerResponse handles turn/steer's reply. A turn that refused
// the message, such as one that just ended, gets it queued instead.
func (u *chatUI) acceptChatSteerResponse(m appMessage, p chatPending) bool {
	if p.method != "turn/steer" {
		return false
	}
	if m.Error == nil || p.queuedPrompt == "" {
		return true
	}
	if err := u.queuePrompt(p.threadID, p.queuedPrompt); err != nil {
		u.printRole("system", "The running turn did not take the message ("+chatText(m.Error.Message)+") and it could not be queued: "+err.Error()+"\n")
		return true
	}
	reason := strings.TrimSpace(chatText(m.Error.Message))
	u.printRole("system", "The running turn did not take the message ("+reason+"); it runs right after the turn.\n")
	return true
}
