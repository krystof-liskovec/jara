package agentrouter

import (
	"fmt"
	"os"
	"path/filepath"

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

// rebriefAfterMove handles a failed Codex resume of a moved conversation:
// the next turn starts a fresh Codex thread briefed on the transcript.
func (t *Thread) rebriefAfterMove() bool {
	if !t.Portable || len(t.Turns) == 0 {
		return false
	}
	t.MarkMoved()
	t.Save()
	return true
}
