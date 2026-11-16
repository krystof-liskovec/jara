package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentrouter"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/portable"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// runnerCheckpoint carries a workspace's conversations between machines:
// every conversation that changed since the receiver last saw it, which one
// was open, and scheduled prompts still waiting to run.
type runnerCheckpoint struct {
	Seq       int64          `json:"seq,omitempty"` // set by the host on export
	Current   string         `json:"current,omitempty"`
	Threads   []runnerBundle `json:"threads,omitempty"`
	Schedules []chatSchedule `json:"schedules,omitempty"` // queued and scheduled prompts
	// Crunch is the active /crunch or /goalcrunch task, if any.
	Crunch json.RawMessage `json:"crunch,omitempty"`
}

// workspaceThreads lists Jara's conversations that ran inside root.
func workspaceThreads(root string) []agentrouter.Thread {
	var threads []agentrouter.Thread
	for _, thread := range agentrouter.ListThreads() {
		if thread.CWD != "" && pathWithin(root, thread.CWD) {
			threads = append(threads, thread)
		}
	}
	return threads
}

// threadVersion changes whenever the conversation record or one of its
// native session files changes, without reading the session files.
func threadVersion(thread *agentrouter.Thread, rollouts map[string]string) string {
	record, _ := json.Marshal(thread)
	hash := sha256.New()
	hash.Write(record)
	for _, path := range nativeSessionPaths(thread, rollouts) {
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(hash, "\x00%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
		}
	}
	return hex.EncodeToString(hash.Sum(nil)[:12])
}

// nativeSessionPaths lists the native session files a conversation uses.
func nativeSessionPaths(thread *agentrouter.Thread, rollouts map[string]string) []string {
	var paths []string
	for _, segment := range thread.Segments {
		switch segment.Agent {
		case protocol.AgentClaude:
			if segment.Started {
				paths = append(paths, portable.ClaudeSessionPath(thread.CWD, segment.BackendID))
			}
		default:
			if rel, ok := rollouts[segment.BackendID]; ok {
				paths = append(paths, filepath.Join(portable.CodexHome(), filepath.FromSlash(rel)))
			}
		}
	}
	return paths
}

// workspaceThreadVersions returns the current version of every conversation.
func workspaceThreadVersions(root string) map[string]string {
	rollouts := portable.IndexCodexRollouts()
	versions := map[string]string{}
	for _, thread := range workspaceThreads(root) {
		versions[thread.ID] = threadVersion(&thread, rollouts)
	}
	return versions
}

// collectCheckpoint exports the conversations whose version differs from
// known, and returns every current version. current is exported even when
// Jara has no record of it yet (a Codex conversation started before).
func collectCheckpoint(root, writer, current string, known map[string]string, sent *runnerSentState) (runnerCheckpoint, map[string]string, error) {
	rollouts := portable.IndexCodexRollouts()
	checkpoint := runnerCheckpoint{Current: current}
	versions := map[string]string{}
	total := 0
	seenCurrent := false
	for _, thread := range workspaceThreads(root) {
		seenCurrent = seenCurrent || thread.ID == current
		version := threadVersion(&thread, rollouts)
		versions[thread.ID] = version
		if known[thread.ID] == version {
			continue
		}
		bundle, err := exportThread(&thread, root, writer, rollouts, sent)
		if err != nil {
			return runnerCheckpoint{}, nil, err
		}
		for _, file := range bundle.Files {
			total += file.size()
		}
		if total > maxTransfer {
			return runnerCheckpoint{}, nil, fmt.Errorf("conversation files exceed %d MiB", maxTransfer>>20)
		}
		checkpoint.Threads = append(checkpoint.Threads, bundle)
	}
	if current != "" && !seenCurrent && portable.ValidBackendID(current) {
		bundle, err := exportConversation(current, root, writer)
		if err != nil {
			return runnerCheckpoint{}, nil, err
		}
		checkpoint.Threads = append(checkpoint.Threads, bundle)
	}
	return checkpoint, versions, nil
}

// importCheckpoint installs every conversation of a checkpoint for root and
// reports the ones that continue from a briefing instead of their native
// session.
func importCheckpoint(checkpoint runnerCheckpoint, root string) ([]string, error) {
	var briefed []string
	for _, bundle := range checkpoint.Threads {
		native, err := importConversation(bundle, root)
		if err != nil {
			return briefed, fmt.Errorf("conversation %s: %w", bundle.ThreadID, err)
		}
		if !native {
			briefed = append(briefed, bundle.ThreadID)
		}
	}
	return briefed, nil
}

// pendingSchedules reads scheduled prompts that have not run yet.
func pendingSchedules(path string) []chatSchedule {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var jobs, pending []chatSchedule
	if json.Unmarshal(data, &jobs) != nil {
		return nil
	}
	for _, job := range jobs {
		if job.Status == "pending" {
			pending = append(pending, job)
		}
	}
	return pending
}

// replaceSchedules makes incoming the pending scheduled prompts of a session
// that is not running, keeping its finished history.
func replaceSchedules(path string, incoming []chatSchedule) error {
	var jobs []chatSchedule
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &jobs)
	}
	kept := jobs[:0]
	for _, job := range jobs {
		if job.Status != "pending" {
			kept = append(kept, job)
		}
	}
	for _, job := range incoming {
		if job.Status == "pending" && job.ID != "" {
			kept = append(kept, job)
		}
	}
	if len(kept) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return fsutil.WriteJSON(path, kept)
}

// activeCrunch reads a session's /crunch task while it is still active.
func activeCrunch(path string) json.RawMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var job chatCrunchJob
	if json.Unmarshal(data, &job) != nil || !crunchJobActive(job.Status) {
		return nil
	}
	return data
}

// installCrunch makes task the /crunch task of a session that is not
// running; a running task resumes from the saved conversation when the
// session starts. Without a task, a stale one here is removed.
func installCrunch(path string, task json.RawMessage) error {
	if len(task) == 0 {
		if activeCrunch(path) != nil {
			return os.Remove(path)
		}
		return nil
	}
	var job chatCrunchJob
	if err := json.Unmarshal(task, &job); err != nil {
		return fmt.Errorf("read moved crunch task: %w", err)
	}
	return fsutil.WriteJSON(path, job)
}
