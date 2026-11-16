package jara

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/portable"
)

// runnerClaim answers a device asking to run a workspace's agent. Wait means
// the current runner is busy and hands the workspace over when its turn ends.
type runnerClaim struct {
	Wait         bool   `json:"wait,omitempty"`
	Runner       string `json:"runner,omitempty"` // the runner being waited for
	Epoch        int64  `json:"epoch,omitempty"`
	Seq          int64  `json:"seq,omitempty"`
	Writer       string `json:"writer,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Socket       string `json:"socket,omitempty"`
	Root         string `json:"root,omitempty"`
	// Changed lists the conversations checkpointed after the claim's Since.
	Changed []string `json:"changed,omitempty"`
}

// runnerCheckpointAnswer is the host's answer to a checkpoint: its number, or
// the session files that did not match, when it stored nothing.
type runnerCheckpointAnswer struct {
	Seq    int64          `json:"seq,omitempty"`
	Resync []runnerResync `json:"resync,omitempty"`
}

type runnerStatus struct {
	Lease  runnerLease `json:"lease"`
	Online bool        `json:"online"`
	// Standbys is what this host last forwarded to each standby, in rank.
	Standbys []namedStandbyStatus `json:"standbys,omitempty"`
}

type namedStandbyStatus struct {
	Name string `json:"name"`
	standbyStatus
}

func runnerRPC(q Request) error {
	paths, err := runnerPathSet(q.ID)
	if err != nil {
		return err
	}
	out := json.NewEncoder(os.Stdout)
	switch q.Op {
	case "runner-status":
		lease, _, err := loadRunnerLease(paths, q.ID)
		if err != nil {
			return err
		}
		status := runnerStatus{Lease: lease, Online: lease.online()}
		statuses := loadStandbyStatuses(paths)
		for _, standby := range loadStandbys() {
			entry := statuses[standby.Name]
			entry.Sent = nil
			status.Standbys = append(status.Standbys, namedStandbyStatus{Name: standby.Name, standbyStatus: entry})
		}
		return out.Encode(status)
	case "runner-prefer":
		for _, device := range q.Devices {
			if device == "" || deviceNameRE.MatchString(device) {
				return fmt.Errorf("invalid device name %q", device)
			}
		}
		_, err = updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
			lease.Prefers = q.Devices
			return nil
		})
		return err
	case "runner-claim":
		claim, err := claimRunner(q, paths)
		if err != nil {
			return err
		}
		return out.Encode(claim)
	case "runner-export":
		var request runnerExportRequest
		if q.Compress {
			if err = decodeMaybeCompressed(os.Stdin, maxTransfer, &request); err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("read export request: %w", err)
			}
		}
		checkpoint, err := exportRunnerCheckpoint(paths, q.ID, q.Since, request.Haves)
		if err != nil {
			return err
		}
		return out.Encode(checkpoint) // compressed by serveCompressedRPC
	case "runner-checkpoint":
		var checkpoint runnerCheckpoint
		if err = decodeMaybeCompressed(os.Stdin, maxTransfer*2, &checkpoint); err != nil {
			return fmt.Errorf("read checkpoint: %w", err)
		}
		lease, err := updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
			if !lease.holds(q.Runner, q.Epoch) {
				return errRunnerLost
			}
			lease.LastSeen = time.Now().Unix()
			return storeRunnerCheckpoint(paths, lease, checkpoint, q.Runner)
		})
		var resync *runnerResyncError
		if errors.As(err, &resync) {
			// Nothing was stored; the device continues each file from here.
			return out.Encode(runnerCheckpointAnswer{Resync: resync.files})
		}
		if err != nil {
			return err
		}
		triggerStandbyForward(q.ID, paths)
		return out.Encode(runnerCheckpointAnswer{Seq: lease.Seq})
	case "runner-release", "runner-tunnel":
		var socket string
		_, err = updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
			if !lease.holds(q.Runner, q.Epoch) {
				return errRunnerLost
			}
			lease.LastSeen = time.Now().Unix()
			if q.Op == "runner-release" {
				lease.Owner, lease.Link, lease.Busy, lease.Pending = "", "", false, false
				return nil
			}
			// Only this lease's device uses this socket, so an existing one is
			// left from its earlier, dropped connection. sshd may not replace it.
			socket = paths.socket(lease.Epoch)
			_ = os.Remove(socket)
			return nil
		})
		if err != nil || q.Op == "runner-release" {
			if err == nil {
				triggerStandbyForward(q.ID, paths) // standbys learn the device stopped
			}
			return err
		}
		return out.Encode(map[string]string{"socket": socket})
	}
	return fmt.Errorf("unknown RPC operation %s", q.Op)
}

// storeRunnerCheckpoint saves conversations and scheduled prompts written by
// writer as the next checkpoint. The caller holds the lease lock.
func storeRunnerCheckpoint(paths runnerPaths, lease *runnerLease, checkpoint runnerCheckpoint, writer string) error {
	if err := os.MkdirAll(paths.threads, 0700); err != nil {
		return err
	}
	if err := checkNativeFiles(paths, checkpoint.Threads); err != nil {
		return err
	}
	lease.Seq++
	if lease.Threads == nil {
		lease.Threads = map[string]int64{}
	}
	for _, bundle := range checkpoint.Threads {
		if !portable.ValidBackendID(bundle.ThreadID) {
			return fmt.Errorf("invalid conversation ID in checkpoint")
		}
		bundle.Writer = writer
		if err := storeNativeFiles(paths, &bundle); err != nil {
			return err
		}
		if err := fsutil.WriteJSON(paths.thread(bundle.ThreadID), bundle); err != nil {
			return err
		}
		lease.Threads[bundle.ThreadID] = lease.Seq
	}
	if err := fsutil.WriteJSON(paths.thread(".schedules"), checkpoint.Schedules); err != nil {
		return err
	}
	crunch := checkpoint.Crunch
	if len(crunch) == 0 {
		crunch = json.RawMessage("null")
	}
	if err := os.WriteFile(paths.thread(".crunch"), crunch, 0600); err != nil {
		return err
	}
	lease.Writer, lease.CheckpointAt = writer, time.Now().Unix()
	if checkpoint.Current != "" {
		lease.Conversation = checkpoint.Current
	}
	if writer == runnerHub {
		lease.HubSeq = lease.Seq
	}
	return nil
}

// exportRunnerCheckpoint returns the conversations changed after checkpoint
// since, with the pending scheduled prompts of the latest checkpoint.
func exportRunnerCheckpoint(paths runnerPaths, id string, since int64, haves []runnerHave) (runnerCheckpoint, error) {
	lease, _, err := loadRunnerLease(paths, id)
	if err != nil {
		return runnerCheckpoint{}, err
	}
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
		loadNativeFiles(paths, &bundle, haves)
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

// checkpointHubConversations saves the host's changed conversations so a
// device can continue them. The caller holds the lease lock.
func checkpointHubConversations(id, root string, paths runnerPaths, lease *runnerLease) error {
	current := lease.Conversation
	daemon, err := chatDaemonPathSet(id)
	if err == nil {
		if state, ok, _ := readSavedChatDaemonState(id, daemon); ok && state.Conversation != "" {
			current = state.Conversation
		}
	}
	checkpoint, versions, err := collectCheckpoint(root, runnerHub, current, lease.HubVersions, nil)
	if err != nil {
		return err
	}
	if daemon.schedules != "" {
		checkpoint.Schedules = pendingSchedules(daemon.schedules)
		checkpoint.Crunch = activeCrunch(daemon.crunch)
	}
	lease.HubVersions = versions
	if len(checkpoint.Threads) == 0 && len(checkpoint.Schedules) == 0 && len(checkpoint.Crunch) == 0 && (current == "" || current == lease.Conversation) {
		return nil // nothing changed here since the last checkpoint
	}
	if err = storeRunnerCheckpoint(paths, lease, checkpoint, runnerHub); err != nil {
		return err
	}
	triggerStandbyForward(id, paths)
	return nil
}

// importHubCheckpoint installs conversations that devices checkpointed since
// the host last ran the workspace. The caller holds the lease lock.
func importHubCheckpoint(id string, paths runnerPaths, lease *runnerLease) error {
	if lease.Seq <= lease.HubSeq {
		return nil
	}
	workspace, err := workspaceInfo(id)
	if err != nil {
		return err
	}
	checkpoint, err := exportRunnerCheckpoint(paths, id, lease.HubSeq, nil)
	if err != nil {
		return err
	}
	briefed, err := importCheckpoint(checkpoint, workspace.Path)
	if err != nil {
		return err
	}
	if len(briefed) > 0 {
		fmt.Fprintf(os.Stderr, "%d conversation(s) continue from Jara's transcript; their agent sessions could not be restored here.\n", len(briefed))
	}
	if daemon, pathErr := chatDaemonPathSet(id); pathErr == nil && chatDaemonPID(daemon) == 0 {
		if err = replaceSchedules(daemon.schedules, checkpoint.Schedules); err != nil {
			return err
		}
		if err = installCrunch(daemon.crunch, checkpoint.Crunch); err != nil {
			return err
		}
	}
	lease.HubSeq = lease.Seq
	lease.HubVersions = workspaceThreadVersions(workspace.Path)
	return nil
}
