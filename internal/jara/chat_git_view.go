package jara

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// In a Git workspace the changes pane shows what is uncommitted, read from
// the persistent session with jara/git/diff, so it empties after a commit and
// includes edits made outside the agent. Elsewhere, or when the session
// cannot answer, it shows the edits the agent reported.

const chatGitDiffRefresh = 10 * time.Second // while the pane is on screen

type chatGitDiff struct {
	known, available bool // whether the session answered, and if it is Git
	changes          []protocol.FileChange
	omitted          int
	signature        [32]byte
	requesting       bool
	again            bool // refresh once more when the current reply arrives
	lastRequest      time.Time
}

// visible returns the changes the pane shows.
func (d *chatDiffState) visible() []protocol.FileChange {
	if d.git.available {
		return d.git.changes
	}
	return d.changes
}

// resetAgentChanges forgets the agent's edits before a conversation's
// history is replayed into the pane.
func (d *chatDiffState) resetAgentChanges() {
	d.changes, d.arrived, d.scroll = nil, time.Time{}, 0
}

// refreshGitDiff asks the session for the workspace's uncommitted changes.
// It is best effort: a connection that is closing reports that on its own,
// so a failed write here must not end the interface.
func (u *chatUI) refreshGitDiff() {
	g := &u.diff.git
	if u.chatOffline() || u.c == nil || u.c.encoder == nil || g.known && !g.available {
		return
	}
	if g.requesting {
		g.again = true
		return
	}
	g.requesting, g.lastRequest = true, time.Now()
	if err := u.requestWithAction("jara/git/diff", map[string]any{}, ""); err != nil {
		g.requesting = false
	}
}

// refreshGitDiffWhileShown keeps a visible pane current, so commits made
// elsewhere show up.
func (u *chatUI) refreshGitDiffWhileShown(now time.Time) {
	d := &u.diff
	shown := chatDiffPaneWidth(d, d.screenWidth, d.screenHeight) > 0 || d.fullScreen()
	if shown && d.git.available && now.Sub(d.git.lastRequest) >= chatGitDiffRefresh {
		u.refreshGitDiff()
	}
}

func (u *chatUI) acceptGitDiffResponse(m appMessage) {
	g := &u.diff.git
	g.requesting = false
	defer func() {
		if g.again {
			g.again = false
			u.refreshGitDiff()
		}
	}()
	if m.Error != nil {
		if !g.known {
			// A session from before this feature, or another app-server:
			// keep showing the agent's edits.
			g.known, g.available = true, false
		}
		return
	}
	var result chatGitDiffResult
	if json.Unmarshal(m.Result, &result) != nil {
		return
	}
	first := !g.known
	g.known, g.available, g.omitted = true, result.Git, result.Omitted
	if !result.Git {
		return
	}
	data, _ := json.Marshal(result.Changes)
	signature := sha256.Sum256(data)
	if signature == g.signature && !first {
		return
	}
	g.signature, g.changes = signature, result.Changes
	u.diff.scroll = 0
	if !first && len(result.Changes) > 0 {
		u.diff.arrived = time.Now() // animate a change that just appeared
	}
}

// expireGitDiffRequest gives up on a reply that never came.
func (u *chatUI) expireGitDiffRequest() {
	g := &u.diff.git
	g.requesting = false
	if !g.known {
		g.known, g.available = true, false
	}
}
