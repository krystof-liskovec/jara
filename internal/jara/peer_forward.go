package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// The primary forwards a workspace to its standbys after every checkpoint a
// device or the primary itself stores, in a detached `jara _standby-forward`
// so the device's checkpoint does not wait for it. One forwarder runs per
// workspace; a checkpoint arriving meanwhile makes it go round once more.
// Each standby receives the checkout and the conversations that changed
// since its last forward, with session files as verified deltas.

// standbyStatus is what the primary last forwarded to one standby.
type standbyStatus struct {
	Seq   int64                     `json:"seq"`
	Sent  map[string]runnerSentFile `json:"sent,omitempty"`
	At    int64                     `json:"at,omitempty"`
	Error string                    `json:"error,omitempty"`
}

func standbyStatusPath(paths runnerPaths) string { return paths.lease + ".standbys" }

func loadStandbyStatuses(paths runnerPaths) map[string]standbyStatus {
	statuses := map[string]standbyStatus{}
	if data, err := os.ReadFile(standbyStatusPath(paths)); err == nil {
		_ = json.Unmarshal(data, &statuses)
	}
	return statuses
}

// triggerStandbyForward makes the workspace's forwarder run, if this host
// has standbys.
func triggerStandbyForward(id string, paths runnerPaths) {
	if len(loadStandbys()) == 0 {
		return
	}
	if err := os.WriteFile(paths.lease+".forward-due", nil, 0600); err != nil {
		return
	}
	if forwarderRunning(paths) {
		return
	}
	if _, err := startDetachedJara("_standby-forward", Request{ID: id}, paths.lease+".forward.log"); err != nil {
		fmt.Fprintln(os.Stderr, "jara: start standby forwarding:", err)
	}
}

func forwarderRunning(paths runnerPaths) bool {
	data, err := os.ReadFile(paths.lease + ".forward")
	pid, _ := strconv.Atoi(string(data))
	return err == nil && pid > 1 && pid != os.Getpid() && chatProcessExists(pid)
}

// runStandbyForward forwards workspace id to every standby while forwards
// are due.
func runStandbyForward(id string) error {
	paths, err := runnerPathSet(id)
	if err != nil {
		return err
	}
	if forwarderRunning(paths) {
		return nil
	}
	pidPath, due := paths.lease+".forward", paths.lease+".forward-due"
	if err = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return err
	}
	// Standbys are reached through their pinned SSH configuration.
	_ = os.Setenv("JARA_SSH_CONFIG", peerSSHConfig())
	for {
		if os.Remove(due) != nil {
			// Give up the pid file before looking again, so a trigger that
			// came in between starts a new forwarder or finds this one's file.
			_ = os.Remove(pidPath)
			if _, statErr := os.Stat(due); statErr != nil {
				return nil
			}
			if err = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
				return err
			}
			continue
		}
		for _, standby := range loadStandbys() {
			status := loadStandbyStatuses(paths)[standby.Name]
			if forwardErr := forwardToStandby(id, paths, standby, &status); forwardErr != nil {
				status.Error = forwardErr.Error()
				fmt.Fprintf(os.Stderr, "%s: forward to %s: %v\n", time.Now().Format(time.DateTime), standby.Name, forwardErr)
			} else {
				status.Error, status.At = "", time.Now().Unix()
			}
			unlock, lockErr := lockRunner(paths)
			if lockErr != nil {
				return lockErr
			}
			statuses := loadStandbyStatuses(paths)
			statuses[standby.Name] = status
			err = fsutil.WriteJSON(standbyStatusPath(paths), statuses)
			unlock()
			if err != nil {
				return err
			}
		}
	}
}

// forwardToStandby brings one standby's mirror of the workspace up to date.
func forwardToStandby(id string, paths runnerPaths, standby peerStandby, status *standbyStatus) error {
	remote := Remote{Host: peerAlias(standby.Name)}
	workspace, err := workspaceInfo(id)
	if err != nil {
		return err
	}
	local := checkoutState(workspace.Path)
	var prepared standbyPrepared
	q := Request{Op: "standby-prepare", ID: id, Name: workspace.Name, Branch: local.Branch, Commit: local.Head}
	if err = rpc(remote, q, nil, &prepared); err != nil {
		return err
	}
	if prepared.Busy {
		return fmt.Errorf("an agent works in this workspace on %s; forwarding waits until it stops", standby.Name)
	}
	if prepared.Seq == 0 {
		status.Seq, status.Sent = 0, nil // a new or rebuilt standby gets everything
	}
	if err = forwardCommits(remote, id, workspace.Path, local, prepared); err != nil {
		return fmt.Errorf("commits: %w", err)
	}
	if err = syncWorkspace(remote, Binding{Remote: "peer-" + standby.Name, Workspace: id}, workspace.Path, "push", true, false, true); err != nil {
		return fmt.Errorf("files: %w", err)
	}
	lease, _, err := loadRunnerLease(paths, id)
	if err != nil {
		return err
	}
	_, stored, err := sendCheckpoint(status.Sent, !lease.Busy, standby.Name,
		func(sent *runnerSentState) (runnerCheckpoint, error) {
			return exportStoredCheckpoint(paths, lease, status.Seq, sent)
		},
		func(checkpoint runnerCheckpoint) (runnerCheckpointAnswer, error) {
			var answer runnerCheckpointAnswer
			err := rpcCompressed(remote, Request{Op: "standby-checkpoint", ID: id}, standbyCheckpoint{Checkpoint: checkpoint, Lease: lease}, &answer)
			return answer, err
		})
	if err != nil {
		return fmt.Errorf("conversations: %w", err)
	}
	status.Seq, status.Sent = lease.Seq, stored
	return nil
}

// forwardCommits gives the standby this checkout's branch and commit: the
// commits it lacks as a bundle, or none when only the branch differs.
func forwardCommits(remote Remote, id, root string, local gitCheckoutState, prepared standbyPrepared) error {
	if local.Head == "" || prepared.Head == local.Head && prepared.Branch == local.Branch {
		return nil
	}
	var bundle io.Reader
	if prepared.Head != local.Head {
		var exclude []string
		if prepared.Head != "" && hasCommit(root, prepared.Head) && isAncestor(root, prepared.Head, local.Head) {
			exclude = append(exclude, prepared.Head)
		}
		reader, cleanup, err := repositoryBundle(root, exclude...)
		if err != nil {
			return err
		}
		defer cleanup()
		bundle = reader
	}
	return rpc(remote, Request{Op: "standby-git", ID: id, Branch: local.Branch, Commit: local.Head}, bundle, nil)
}

// exportStoredCheckpoint collects the conversations checkpointed after since
// from this host's store, with session files as deltas against sent.
func exportStoredCheckpoint(paths runnerPaths, lease runnerLease, since int64, sent *runnerSentState) (runnerCheckpoint, error) {
	checkpoint := runnerCheckpoint{Seq: lease.Seq, Current: lease.Conversation}
	for thread, seq := range lease.Threads {
		if seq <= since {
			continue
		}
		data, err := os.ReadFile(paths.thread(thread))
		if err != nil {
			return runnerCheckpoint{}, err
		}
		var bundle runnerBundle
		if err = json.Unmarshal(data, &bundle); err != nil {
			return runnerCheckpoint{}, err
		}
		files := bundle.Files[:0]
		for _, file := range bundle.Files {
			if file.size() == 0 { // stored raw beside the record
				path := paths.storedNativePath(bundle.ThreadID, file)
				info, statErr := os.Stat(path)
				if statErr != nil {
					continue
				}
				state, readErr := readNativeFile(path, info.Size(), sent.Prev, sent.Verify, &file)
				if readErr != nil {
					continue
				}
				sent.Next[path] = state
			}
			files = append(files, file)
		}
		bundle.Files = files
		checkpoint.Threads = append(checkpoint.Threads, bundle)
	}
	if data, err := os.ReadFile(paths.thread(".schedules")); err == nil {
		_ = json.Unmarshal(data, &checkpoint.Schedules)
	}
	if data, err := os.ReadFile(paths.thread(".crunch")); err == nil && string(data) != "null" {
		checkpoint.Crunch = data
	}
	return checkpoint, nil
}
