package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/portable"
	"krystof.liskovec/jara/internal/protocol"
)

// runnerNativeFile is one agent-native session file of a conversation.
type runnerNativeFile struct {
	Agent     string `json:"agent"`
	BackendID string `json:"backend_id"`
	CWD       string `json:"cwd,omitempty"`  // Claude Code's working directory
	Path      string `json:"path,omitempty"` // Codex rollout path under CODEX_HOME
	// The contents: Text when UTF-8, otherwise Data. With Base > 0 they are
	// the bytes after offset Base of a file whose bytes before Base end with
	// the runnerTailSize bytes hashed in Tail.
	Text string `json:"text,omitempty"`
	Data []byte `json:"data,omitempty"`
	Base int64  `json:"base,omitempty"`
	Tail string `json:"tail,omitempty"`
	// Sum is the SHA-256 of the whole file, sent when a turn has ended.
	Sum   string `json:"sum,omitempty"`
	local string // the exporting machine's path
}

// runnerSentState tracks which session file contents the host stores: Prev
// before this checkpoint, Next once the host accepts it. Paths are local.
// Verify adds whole-file checksums.
type runnerSentState struct {
	Prev, Next map[string]runnerSentFile
	Verify     bool
}

// runnerBundle is a conversation checkpoint: Jara's own transcript plus the
// agents' native session files, with paths relative to the exporting Root.
type runnerBundle struct {
	ThreadID string             `json:"thread_id"`
	Root     string             `json:"root"`
	Thread   json.RawMessage    `json:"thread"`
	Files    []runnerNativeFile `json:"files,omitempty"`
	Writer   string             `json:"writer"`
	At       int64              `json:"at"`
}

// exportConversation packages conversation threadID run in workspace root.
func exportConversation(threadID, root, writer string) (runnerBundle, error) {
	if !portable.ValidBackendID(threadID) {
		return runnerBundle{}, fmt.Errorf("invalid conversation ID")
	}
	thread := agentrouter.LoadThread(threadID)
	if thread == nil {
		// A Codex conversation Jara has no record of: Codex's rollout is the
		// whole conversation, and the importer creates the record.
		thread = &agentrouter.Thread{ID: threadID, CWD: root, Created: time.Now().Unix(), Updated: time.Now().Unix(),
			Segments: []agentrouter.ThreadSegment{{Agent: protocol.AgentCodex, BackendID: threadID, StartTurn: 1}}}
	}
	return exportThread(thread, root, writer, portable.IndexCodexRollouts(), nil)
}

// exportThread reads a conversation's record and native session files; with
// sent, only what the host lacks of each file.
func exportThread(thread *agentrouter.Thread, root, writer string, rollouts map[string]string, sent *runnerSentState) (runnerBundle, error) {
	thread.Portable = true
	record, err := json.Marshal(thread)
	if err != nil {
		return runnerBundle{}, err
	}
	bundle := runnerBundle{ThreadID: thread.ID, Root: root, Thread: record, Writer: writer, At: time.Now().Unix()}
	for _, file := range threadNativeFiles(thread, rollouts) {
		path := file.local
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > portable.MaxNativeFile {
			continue
		}
		var prev map[string]runnerSentFile
		verify := false
		if sent != nil {
			prev, verify = sent.Prev, sent.Verify
		}
		state, readErr := readNativeFile(path, info.Size(), prev, verify, &file)
		if readErr != nil {
			continue
		}
		if sent != nil {
			sent.Next[path] = state
		}
		bundle.Files = append(bundle.Files, file)
	}
	return bundle, nil
}

// threadNativeFiles lists a conversation's native session files on this
// machine, each with its local path, whether or not it exists.
func threadNativeFiles(thread *agentrouter.Thread, rollouts map[string]string) []runnerNativeFile {
	var files []runnerNativeFile
	seen := map[string]bool{}
	for _, segment := range thread.Segments {
		key := segment.Agent + "/" + segment.BackendID
		if seen[key] || !portable.ValidBackendID(segment.BackendID) {
			continue
		}
		seen[key] = true
		file := runnerNativeFile{Agent: segment.Agent, BackendID: segment.BackendID}
		switch segment.Agent {
		case protocol.AgentClaude:
			if !segment.Started {
				continue
			}
			file.CWD, file.local = thread.CWD, portable.ClaudeSessionPath(thread.CWD, segment.BackendID)
		default:
			rel, ok := rollouts[segment.BackendID]
			if !ok {
				continue // imported as a briefing instead
			}
			file.Path, file.local = rel, filepath.Join(portable.CodexHome(), filepath.FromSlash(rel))
		}
		files = append(files, file)
	}
	return files
}

// importConversation installs a checkpoint for a workspace checked out at
// root. It reports whether the current agent session was restored natively;
// otherwise the conversation continues from a briefing on its transcript.
func importConversation(bundle runnerBundle, root string) (bool, error) {
	if !portable.ValidBackendID(bundle.ThreadID) || len(bundle.Thread) == 0 {
		return false, fmt.Errorf("checkpoint has no conversation")
	}
	var thread agentrouter.Thread
	if err := json.Unmarshal(portable.RewritePath(bundle.Thread, bundle.Root, root), &thread); err != nil {
		return false, fmt.Errorf("read checkpoint conversation: %w", err)
	}
	if thread.ID != bundle.ThreadID || len(thread.Segments) == 0 {
		return false, fmt.Errorf("checkpoint conversation does not match its ID")
	}
	restored := map[string]bool{}
	for _, file := range bundle.Files {
		if !portable.ValidBackendID(file.BackendID) {
			continue
		}
		var dest string
		switch file.Agent {
		case protocol.AgentClaude:
			cwd := string(portable.RewritePath([]byte(file.CWD), bundle.Root, root))
			dest = portable.ClaudeSessionPath(cwd, file.BackendID)
		case protocol.AgentCodex:
			var err error
			if dest, err = portable.CodexRolloutDestination(file.Path, file.BackendID); err != nil {
				fmt.Fprintln(os.Stderr, "Skipping conversation file:", err)
				continue
			}
		default:
			continue
		}
		if file.Base > 0 {
			if err := appendImportedDelta(dest, bundle.Root, root, file); err != nil {
				return false, err
			}
		} else if err := portable.WriteFileAtomic(dest, portable.RewritePath(file.bytes(), bundle.Root, root)); err != nil {
			return false, fmt.Errorf("install %s session: %w", protocol.AgentDisplayName(file.Agent), err)
		}
		restored[file.Agent+"/"+file.BackendID] = true
	}
	current := thread.Current()
	native := restored[current.Agent+"/"+current.BackendID] || current.Agent == protocol.AgentClaude && !current.Started
	if !native {
		thread.MarkMoved()
	}
	return native, agentrouter.StoreThread(&thread)
}
