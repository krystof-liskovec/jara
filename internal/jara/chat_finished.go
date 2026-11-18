package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"krystof.liskovec/jara/internal/fsutil"
)

// Ctrl+C on an idle conversation means you are done with it: the window
// leaves a marker for its folder, and the next plain `jara` there starts a
// new conversation instead of resuming. `jara resume` (--latest), /resume and
// `jara new` choose for themselves and clear the marker.

func chatFinishedPath(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(fsutil.DataRoot(), "finished", hex.EncodeToString(sum[:8]))
}

// markChatFinished records that the folder's conversation is done.
func markChatFinished(root string) {
	if root == "" {
		return
	}
	path := chatFinishedPath(root)
	if os.MkdirAll(filepath.Dir(path), 0700) == nil {
		_ = os.WriteFile(path, nil, 0600)
	}
}

// applyChatFinished starts a plain open of root on a new conversation when
// its last one was finished with Ctrl+C. Any open clears the marker: an
// explicit resume or new conversation is a choice of its own.
func applyChatFinished(root string, q *Request) {
	if root == "" {
		return
	}
	path := chatFinishedPath(root)
	if _, err := os.Stat(path); err != nil {
		return
	}
	_ = os.Remove(path)
	if q.Conversation == "" && !q.New && !q.Resume && !q.ResumeLatest && !q.Background && !q.Handoff {
		q.New = true
	}
}
