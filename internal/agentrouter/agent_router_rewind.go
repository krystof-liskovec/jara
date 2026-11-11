package agentrouter

import (
	"encoding/json"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
)

// /rewind drops the latest prompts and everything after them. A conversation
// that only ever ran on Codex is reverted natively, so Codex forgets the
// dropped turns with full fidelity. Any other conversation starts its next
// turn in a fresh session briefed on the turns that remain. Files in the
// working tree are never rolled back.

const rewindBriefing = "[Jara: the user rewound this conversation to an earlier point and is continuing from there. " +
	"The exchange up to that point is replayed below. Files in the working tree were not rolled back, so they may " +
	"still include changes from the discarded turns.]\n\n"

type rewindRequest struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"` // first dropped turn, when the UI knows it
	Drop     int    `json:"drop"`   // prompts to drop, counted from the end
}

func (r *agentRouter) rewindThread(id json.RawMessage, params json.RawMessage) error {
	var request rewindRequest
	_ = json.Unmarshal(params, &request)
	if request.ThreadID == "" || request.Drop < 1 {
		r.fail(id, "choose a prompt to rewind to")
		return nil
	}
	thread := r.threads[request.ThreadID]
	if thread == nil {
		thread = LoadThread(request.ThreadID)
		if thread != nil {
			r.threads[thread.ID] = thread
		}
	}
	if thread != nil && thread.claude != nil && thread.claude.turnID != "" {
		r.fail(id, "wait for the running turn to finish before rewinding")
		return nil
	}
	if thread == nil || len(thread.Segments) == 1 && thread.Current().Agent == protocol.AgentCodex {
		return r.revertCodex(id, thread, request)
	}
	thread.dropPrompts(request.Drop)
	if thread.claude != nil {
		thread.claude.stop()
		thread.claude = nil
	}
	thread.Rebrief = true
	thread.Save()
	r.reply(id, map[string]any{})
	return nil
}

func (r *agentRouter) revertCodex(id json.RawMessage, thread *Thread, request rewindRequest) error {
	backend, turnID := request.ThreadID, request.TurnID
	if thread != nil {
		backend = thread.Current().BackendID
		if known := thread.firstDroppedTurnID(request.Drop); known != "" {
			turnID = known
		}
	}
	if turnID == "" {
		r.fail(id, "this prompt's turn is not known yet; try again after the conversation reloads")
		return nil
	}
	revertParams, _ := json.Marshal(map[string]any{"threadId": backend, "beforeTurnId": turnID})
	return r.callCodex(codexCall{method: "thread/revert", callback: func(_, failure json.RawMessage) {
		if failure != nil {
			r.fail(id, "Codex could not rewind the conversation: "+rpcErrorText(failure))
			return
		}
		if thread != nil {
			thread.dropPrompts(request.Drop)
			thread.Save()
		}
		r.reply(id, map[string]any{})
	}}, revertParams)
}

// dropPrompts removes turns from the end until drop user prompts are gone.
func (t *Thread) dropPrompts(drop int) {
	cut := t.promptCut(drop)
	t.Turns = t.Turns[:cut]
	for i := range t.Segments {
		if t.Segments[i].StartTurn > cut+1 {
			t.Segments[i].StartTurn = cut + 1
		}
	}
}

func (t *Thread) promptCut(drop int) int {
	cut := len(t.Turns)
	for cut > 0 && drop > 0 {
		cut--
		if strings.TrimSpace(t.Turns[cut].User) != "" {
			drop--
		}
	}
	return cut
}

func (t *Thread) firstDroppedTurnID(drop int) string {
	if cut := t.promptCut(drop); cut < len(t.Turns) {
		return t.Turns[cut].ID
	}
	return ""
}

// compactClaude asks Claude Code to compact its context with its own
// /compact command, shown in the UI like Codex's compaction.
func (r *agentRouter) compactClaude(id json.RawMessage, thread *Thread) {
	s := thread.claude
	if s.turnID != "" {
		r.fail(id, "wait for the running turn to finish before compacting")
		return
	}
	if !thread.Current().Started || thread.Rebrief {
		r.fail(id, "there is nothing to compact yet")
		return
	}
	s.compacting = true
	r.reply(id, map[string]any{})
	_ = s.startTurn(nil, "/compact")
	if s.turnID != "" {
		s.r.notify("item/started", map[string]any{"threadId": thread.ID, "turnId": s.turnID, "item": map[string]any{"id": s.turnID + "-compact", "type": "contextCompaction"}})
	}
}

// finishCompaction reports the end of a Claude Code /compact turn.
func (s *claudeSession) finishCompaction() {
	if !s.compacting {
		return
	}
	s.compacting = false
	s.r.notify("item/completed", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "item": map[string]any{"id": s.turnID + "-compact", "type": "contextCompaction"}})
	s.contextTokens = 0
}
