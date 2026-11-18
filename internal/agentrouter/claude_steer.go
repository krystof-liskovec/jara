package agentrouter

import (
	"encoding/json"

	"krystof.liskovec/jara/internal/protocol"
)

// Claude Code reads user messages from stdin while a turn runs and takes them
// into the turn at its next model call, as turn/steer does for Codex. A
// message that arrives as the turn is ending is run by Claude Code as a turn
// of its own; the router then opens a continuation turn, so that reply is
// shown instead of dropped.

// steer writes a message into the running turn.
func (s *claudeSession) steer(id json.RawMessage, params json.RawMessage) {
	var request struct {
		ExpectedTurnID string `json:"expectedTurnId"`
	}
	_ = json.Unmarshal(params, &request)
	if s.turnID == "" || s.compacting || request.ExpectedTurnID != s.turnID {
		s.r.fail(id, "no running Claude Code turn to add the message to")
		return
	}
	text := turnText(params)
	content := append([]map[string]any{{"type": "text", "text": text}}, turnImages(params)...)
	if err := s.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}}); err != nil {
		s.r.fail(id, "send the message to Claude Code: "+err.Error())
		return
	}
	s.unanswered, s.steerText = true, text
	if turn := s.thread.lastTurn(); turn != nil && text != "" {
		turn.User += "\n\n" + text
		s.thread.Save()
	}
	s.r.reply(id, map[string]any{"turnId": s.turnID})
}

// continueSteeredTurn opens the turn Claude Code runs for a message the
// ending turn did not reach.
func (s *claudeSession) continueSteeredTurn() {
	text := s.steerText
	s.unanswered, s.steerText = false, ""
	if s.turnID != "" || s.cmd == nil {
		return
	}
	s.thread.beginTurn(text, protocol.AgentClaude)
	s.openTurn(nil)
}
