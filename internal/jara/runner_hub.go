package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/portable"
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
}

type runnerStatus struct {
	Lease  runnerLease `json:"lease"`
	Online bool        `json:"online"`
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
		return out.Encode(runnerStatus{Lease: lease, Online: lease.online()})
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
		checkpoint, err := exportRunnerCheckpoint(paths, q.ID, q.Since)
		if err != nil {
			return err
		}
		if q.Compress {
			data, err := compressedJSON(checkpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(data)
			return err
		}
		return out.Encode(checkpoint)
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
		if err != nil {
			return err
		}
		return out.Encode(map[string]int64{"seq": lease.Seq})
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
func exportRunnerCheckpoint(paths runnerPaths, id string, since int64) (runnerCheckpoint, error) {
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
		loadNativeFiles(paths, &bundle)
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
	return storeRunnerCheckpoint(paths, lease, checkpoint, runnerHub)
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
	checkpoint, err := exportRunnerCheckpoint(paths, id, lease.HubSeq)
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
