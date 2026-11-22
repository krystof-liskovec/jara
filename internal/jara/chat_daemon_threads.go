package jara

import "encoding/json"

// A session serves one conversation at a time, but its windows may have
// different ones open: a phone attached through the host can open another
// conversation than the laptop typing in this one. The session therefore
// follows what windows do rather than what they open:
//
//   - A prompt typed in a conversation makes it the session's conversation
//     (Conversation), the one new windows open and scheduled work belongs to.
//   - The running turn is tracked by its own thread (TurnThread), so its end
//     is seen even when another window opened a different conversation
//     meanwhile; the session never stays busy after a turn ended.
//   - Queue, schedule and crunch requests may name any conversation a window
//     opened in this session, not only the latest.
//
// Messages to subagents carry jaraSubagent and start turns in their own
// threads without affecting any of this.

// chatDaemonTurnTarget is the thread a window's turn/start addresses, and
// whether it is a message to a subagent.
func chatDaemonTurnTarget(params json.RawMessage) (thread string, subagent bool) {
	var target struct {
		ThreadID string `json:"threadId"`
		Subagent bool   `json:"jaraSubagent"`
	}
	_ = json.Unmarshal(params, &target)
	return target.ThreadID, target.Subagent
}

// startConversationTurn records a window's prompt in thread. The caller holds
// the state lock.
func (s *chatDaemonState) startConversationTurn(thread string) {
	s.ActiveTurn = true
	s.TurnThread = thread
	if thread != "" && thread != s.Conversation {
		s.Conversation = thread
		s.ThreadLoaded = true // a window opened it in this session to type in it
	}
}

// conversationTurnEvent reports whether a turn/started or turn/completed in
// thread is the conversation's turn rather than a subagent's. The caller holds
// the state lock.
func (s *chatDaemonState) conversationTurnEvent(thread string) bool {
	if thread == "" {
		return true
	}
	if s.ActiveTurn && s.TurnThread != "" {
		return thread == s.TurnThread
	}
	return s.Conversation == "" || thread == s.Conversation
}

// noteOpened remembers a conversation a window opened in this session. The
// caller holds the state lock.
func (s *chatDaemonState) noteOpened(thread string) {
	if s.opened == nil {
		s.opened = map[string]bool{}
	}
	s.opened[thread] = true
}

// requestThread is the conversation a queue, schedule or crunch request acts
// on: the one it names when a window opened it in this session, otherwise the
// session's conversation (which rejects a request naming another). The caller
// holds the state lock.
func (s *chatDaemonState) requestThread(params json.RawMessage) string {
	var request struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &request) == nil && request.ThreadID != "" && s.opened[request.ThreadID] {
		return request.ThreadID
	}
	return s.Conversation
}
