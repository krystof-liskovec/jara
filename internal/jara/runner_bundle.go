package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentrouter"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/portable"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// runnerNativeFile is one agent-native session file of a conversation.
type runnerNativeFile struct {
	Agent     string `json:"agent"`
	BackendID string `json:"backend_id"`
	CWD       string `json:"cwd,omitempty"`  // Claude Code's working directory
	Path      string `json:"path,omitempty"` // Codex rollout path under CODEX_HOME
	Data      []byte `json:"data"`
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
	thread.Portable = true
	record, err := json.Marshal(thread)
	if err != nil {
		return runnerBundle{}, err
	}
	bundle := runnerBundle{ThreadID: threadID, Root: root, Thread: record, Writer: writer, At: time.Now().Unix()}
	seen := map[string]bool{}
	var total int
	for _, segment := range thread.Segments {
		if seen[segment.Agent+"/"+segment.BackendID] || !portable.ValidBackendID(segment.BackendID) {
			continue
		}
		seen[segment.Agent+"/"+segment.BackendID] = true
		file := runnerNativeFile{Agent: segment.Agent, BackendID: segment.BackendID}
		var path string
		switch segment.Agent {
		case protocol.AgentClaude:
			if !segment.Started {
				continue
			}
			file.CWD, path = thread.CWD, portable.ClaudeSessionPath(thread.CWD, segment.BackendID)
		default:
			if path, file.Path, err = portable.FindCodexRollout(segment.BackendID); err != nil {
				continue // imported as a briefing instead
			}
		}
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > portable.MaxNativeFile {
			continue
		}
		if file.Data, err = os.ReadFile(path); err != nil {
			continue
		}
		if total += len(file.Data); total > maxTransfer {
			return runnerBundle{}, fmt.Errorf("conversation files exceed %d MiB", maxTransfer>>20)
		}
		bundle.Files = append(bundle.Files, file)
	}
	return bundle, nil
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
		data := portable.RewritePath(file.Data, bundle.Root, root)
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
		if err := portable.WriteFileAtomic(dest, data); err != nil {
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
