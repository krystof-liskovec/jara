package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

// runnerClaim answers a device that now runs a workspace's agent.
type runnerClaim struct {
	Epoch        int64  `json:"epoch"`
	Seq          int64  `json:"seq"`
	Writer       string `json:"writer,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Socket       string `json:"socket"`
	Root         string `json:"root"`
}

type runnerStatus struct {
	Lease  runnerLease `json:"lease"`
	Online bool        `json:"online"`
}

const runnerReleaseTimeout = 90 * time.Second

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
		status := runnerStatus{Lease: lease}
		if conn, dialErr := dialRunner(paths, lease); dialErr == nil {
			conn.Close()
			status.Online = true
		}
		return out.Encode(status)
	case "runner-claim":
		claim, err := claimRunner(q, paths)
		if err != nil {
			return err
		}
		return out.Encode(claim)
	case "runner-export":
		var bundle runnerBundle
		data, err := os.ReadFile(paths.bundle)
		if err != nil {
			return fmt.Errorf("this workspace has no conversation checkpoint yet")
		}
		if err = json.Unmarshal(data, &bundle); err != nil {
			return err
		}
		return out.Encode(bundle)
	}
	unlock, err := lockRunner(paths)
	if err != nil {
		return err
	}
	defer unlock()
	lease, _, err := loadRunnerLease(paths, q.ID)
	if err != nil {
		return err
	}
	if !lease.holds(q.Runner, q.Epoch) {
		return errRunnerLost
	}
	lease.LastSeen = time.Now().Unix()
	switch q.Op {
	case "runner-release":
		lease.Owner = ""
		return saveRunnerLease(paths, lease)
	case "runner-tunnel":
		// Only this lease's device uses this socket, so an existing one is left
		// from its earlier, dropped connection. sshd does not replace it.
		socket := paths.socket(lease.Epoch)
		_ = os.Remove(socket)
		if err = saveRunnerLease(paths, lease); err != nil {
			return err
		}
		return out.Encode(map[string]string{"socket": socket})
	case "runner-checkpoint":
		var bundle runnerBundle
		if err = json.NewDecoder(io.LimitReader(os.Stdin, maxTransfer*2)).Decode(&bundle); err != nil {
			return fmt.Errorf("read checkpoint: %w", err)
		}
		if err = storeRunnerCheckpoint(paths, &lease, bundle, q.Runner); err != nil {
			return err
		}
		return out.Encode(map[string]int64{"seq": lease.Seq})
	}
	return fmt.Errorf("unknown RPC operation %s", q.Op)
}

func storeRunnerCheckpoint(paths runnerPaths, lease *runnerLease, bundle runnerBundle, writer string) error {
	bundle.Writer = writer
	if err := fsutil.WriteJSON(paths.bundle, bundle); err != nil {
		return err
	}
	lease.Seq++
	lease.Writer, lease.CheckpointAt, lease.Conversation = writer, time.Now().Unix(), bundle.ThreadID
	if writer == runnerHub {
		lease.HubSeq = lease.Seq
	}
	return saveRunnerLease(paths, *lease)
}

// claimRunner gives the workspace to device q.Runner. A live device is asked
// to release it after its final checkpoint; an unreachable one is replaced
// only with q.Force. The host's own idle session is stopped and checkpointed.
func claimRunner(q Request, paths runnerPaths) (runnerClaim, error) {
	if q.Runner == "" || q.Runner == runnerHub {
		return runnerClaim{}, fmt.Errorf("invalid runner name")
	}
	workspace, err := workspaceInfo(q.ID)
	if err != nil {
		return runnerClaim{}, err
	}
	unlock, err := lockRunner(paths)
	if err != nil {
		return runnerClaim{}, err
	}
	defer func() { unlock() }()
	lease, _, err := loadRunnerLease(paths, q.ID)
	if err != nil {
		return runnerClaim{}, err
	}
	if lease.Owner != "" && lease.Owner != q.Runner {
		if conn, dialErr := dialRunner(paths, lease); dialErr == nil {
			previous := lease
			releaseErr := requestRunnerRelease(conn)
			unlock()
			if releaseErr == nil {
				lease = awaitRunnerRelease(paths, q.ID, previous)
			}
			if unlock, err = lockRunner(paths); err != nil {
				unlock = func() {}
				return runnerClaim{}, err
			}
			if lease, _, err = loadRunnerLease(paths, q.ID); err != nil {
				return runnerClaim{}, err
			}
			if lease.Owner != "" && lease.Owner != q.Runner {
				return runnerClaim{}, fmt.Errorf("%s is still running this conversation (a turn or scheduled work is in progress); try again when it is idle", lease.Owner)
			}
		} else if !q.Force {
			return runnerClaim{}, offlineRunnerError(lease)
		} else {
			fmt.Fprintf(os.Stderr, "Taking over from %s at its checkpoint from %s.\n", lease.Owner, describeRunnerTime(lease.CheckpointAt))
		}
	}
	if err = releaseIdleChatDaemon(q.ID); err != nil {
		return runnerClaim{}, fmt.Errorf("the remote host is running this workspace's agent: %w", err)
	}
	if lease.LastRunner == runnerHub || lease.LastRunner == "" {
		if err = checkpointHubConversation(q.ID, workspace.Path, paths, &lease); err != nil {
			fmt.Fprintln(os.Stderr, "Could not checkpoint the remote host's conversation:", err)
		}
	}
	now := time.Now().Unix()
	lease.Owner, lease.Epoch, lease.ClaimedAt, lease.LastSeen, lease.LastRunner = q.Runner, lease.Epoch+1, now, now, q.Runner
	if err = saveRunnerLease(paths, lease); err != nil {
		return runnerClaim{}, err
	}
	return runnerClaim{Epoch: lease.Epoch, Seq: lease.Seq, Writer: lease.Writer, Conversation: lease.Conversation,
		Socket: paths.socket(lease.Epoch), Root: workspace.Path}, nil
}

// requestRunnerRelease asks a device's session, through its tunnel, to save
// a final checkpoint and give up the lease. A busy session ignores it.
func requestRunnerRelease(conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
		return err
	}
	return json.NewEncoder(conn).Encode(map[string]string{"method": "jara/release"})
}

func awaitRunnerRelease(paths runnerPaths, id string, previous runnerLease) runnerLease {
	deadline := time.Now().Add(runnerReleaseTimeout)
	for time.Now().Before(deadline) {
		lease, _, err := loadRunnerLease(paths, id)
		if err == nil && (lease.Owner != previous.Owner || lease.Epoch != previous.Epoch) {
			return lease
		}
		if conn, err := dialRunner(paths, previous); err != nil {
			break // the session ended without releasing
		} else {
			conn.Close()
		}
		time.Sleep(250 * time.Millisecond)
	}
	lease, _, _ := loadRunnerLease(paths, id)
	return lease
}

// checkpointHubConversation saves the host's current conversation so a device
// can continue it.
func checkpointHubConversation(id, root string, paths runnerPaths, lease *runnerLease) error {
	thread := lease.Conversation
	if daemon, err := chatDaemonPathSet(id); err == nil {
		if state, ok, _ := readSavedChatDaemonState(id, daemon); ok && state.Conversation != "" {
			thread = state.Conversation
		}
	}
	if thread == "" {
		return nil
	}
	bundle, err := exportConversation(thread, root, runnerHub)
	if err != nil {
		return err
	}
	return storeRunnerCheckpoint(paths, lease, bundle, runnerHub)
}

// hubRunnerRoute prepares a session request on the remote host. It returns a
// connection to the device running the workspace, or nil when the host should
// run the agent itself, first importing a newer device checkpoint.
func hubRunnerRoute(q *Request) (net.Conn, error) {
	paths, err := runnerPathSet(q.ID)
	if err != nil {
		return nil, err
	}
	if lease, exists, err := loadRunnerLease(paths, q.ID); err != nil || !exists {
		return nil, err
	} else if lease.Owner != "" {
		if conn, dialErr := dialRunner(paths, lease); dialErr == nil {
			return conn, nil
		}
		if !q.RunnerTakeover {
			return nil, offlineRunnerError(lease)
		}
	}
	unlock, err := lockRunner(paths)
	if err != nil {
		return nil, err
	}
	defer unlock()
	lease, _, err := loadRunnerLease(paths, q.ID)
	if err != nil {
		return nil, err
	}
	if lease.Owner != "" {
		if conn, dialErr := dialRunner(paths, lease); dialErr == nil {
			return conn, nil
		}
		if !q.RunnerTakeover {
			return nil, offlineRunnerError(lease)
		}
		fmt.Fprintf(os.Stderr, "Taking over from %s at its checkpoint from %s.\n", lease.Owner, describeRunnerTime(lease.CheckpointAt))
		lease.Owner, lease.Epoch = "", lease.Epoch+1
	}
	if lease.Seq > lease.HubSeq {
		if err = importHubCheckpoint(q.ID, paths); err != nil {
			fmt.Fprintln(os.Stderr, "Could not restore the conversation checkpoint:", err)
		} else {
			lease.HubSeq = lease.Seq
			if q.Conversation == "" && !q.New {
				q.Conversation = lease.Conversation
				q.Resume, q.ResumeLatest = false, false
			}
		}
	}
	lease.LastRunner = runnerHub
	return nil, saveRunnerLease(paths, lease)
}

func importHubCheckpoint(id string, paths runnerPaths) error {
	workspace, err := workspaceInfo(id)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(paths.bundle)
	if err != nil {
		return err
	}
	var bundle runnerBundle
	if err = json.Unmarshal(data, &bundle); err != nil {
		return err
	}
	native, err := importConversation(bundle, workspace.Path)
	if err == nil && !native {
		fmt.Fprintln(os.Stderr, "The agent's own session could not be restored; the conversation continues from Jara's transcript.")
	}
	return err
}
