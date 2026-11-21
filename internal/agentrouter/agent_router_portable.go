package agentrouter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/portable"
	"krystof.liskovec/jara/internal/protocol"
)

// PortableThreadsEnv makes the router keep a transcript for every
// conversation, so a runner can move it to another machine.
const PortableThreadsEnv = "JARA_PORTABLE_THREADS"

const movedBriefing = "[Jara: this conversation moved to another machine and its earlier agent session could not be restored there. " +
	"The exchange so far is replayed below; files in the working tree already include every change made so far. Continue from here.]\n\n"

func portableThreads() bool { return os.Getenv(PortableThreadsEnv) == "1" }

// StoreThread writes a conversation record received from another machine.
// The record stays portable so later turns keep updating it.
func StoreThread(t *Thread) error {
	if t == nil || !routedThreadIDRE.MatchString(t.ID) || len(t.Segments) == 0 {
		return fmt.Errorf("invalid conversation record")
	}
	t.Portable = true
	return fsutil.WriteJSON(filepath.Join(agentThreadsDir(), t.ID+".json"), t)
}

// MarkMoved makes the next turn start a fresh agent session briefed on the
// transcript, used when the native session could not be imported.
func (t *Thread) MarkMoved() { t.Rebrief, t.RebriefMoved = true, true }

// checkMovedClaudeSession falls back to a briefing when a moved Claude Code
// session is missing here, instead of letting `claude --resume` fail.
func (t *Thread) checkMovedClaudeSession() {
	if !t.Portable || t.Rebrief {
		return
	}
	segment := t.Current()
	if segment.Agent == protocol.AgentClaude && segment.Started && !portable.ClaudeSessionExists(t.CWD, segment.BackendID) {
		if t.claude != nil {
			t.claude.stop()
			t.claude = nil
		}
		t.MarkMoved()
	}
}

// rebriefAfterMove lets the next turn open a fresh Codex thread when a
// portable conversation's native session cannot be resumed.
func (t *Thread) rebriefAfterMove(failure json.RawMessage) bool {
	if !t.Portable {
		return false
	}
	if len(t.Turns) == 0 {
		// A saved conversation can have metadata before its first rollout.
		// Keep other resume errors visible when there is no transcript to replay.
		if !missingRollout(failure, t.Current().BackendID) {
			return false
		}
	}
	t.MarkMoved()
	t.Save()
	return true
}

// missingRollout reports Codex's resume error for a thread that was started
// but never wrote a rollout, which Codex does only once a turn runs.
func missingRollout(failure json.RawMessage, backendID string) bool {
	var err struct {
		Message string `json:"message"`
	}
	return json.Unmarshal(failure, &err) == nil && err.Message == "no rollout found for thread id "+backendID
}

// resumeUntrackedCodexThread resumes a Codex conversation Jara keeps no
// record of. One that was opened but never ran a turn has no rollout to
// resume, so the conversation keeps its ID and its first turn opens a fresh
// Codex thread instead of leaving the window without a conversation.
func (r *agentRouter) resumeUntrackedCodexThread(id json.RawMessage, threadID string, params json.RawMessage) error {
	call := codexCall{method: "thread/resume"}
	call.callback = func(result, failure json.RawMessage) {
		if failure == nil || !routedThreadIDRE.MatchString(threadID) || !missingRollout(failure, threadID) {
			r.send(rpcMessage{ID: id, Result: r.postProcessCodexResult(call, result, failure), Error: failure})
			return
		}
		var request struct {
			CWD string `json:"cwd"`
		}
		_ = json.Unmarshal(params, &request)
		thread := &Thread{ID: threadID, CWD: request.CWD, Created: time.Now().Unix(), Rebrief: true, Portable: portableThreads(),
			Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: threadID, StartTurn: 1}}}
		thread.selectReviewer(params)
		r.threads[threadID] = thread
		r.codexToVirtual[threadID] = threadID
		thread.Save()
		r.reply(id, map[string]any{"thread": map[string]any{"id": threadID, "turns": []any{}}})
	}
	return r.callCodex(call, params)
}
