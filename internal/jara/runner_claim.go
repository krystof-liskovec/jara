package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// claimRunner gives the workspace to device q.Runner.
//
//   - Another online device is asked to hand over; it does so when idle,
//     after a final checkpoint. A passive claim (a window reconnecting by
//     itself) never asks another device and only watches it.
//   - An offline device is replaced; the claimant continues from its last
//     checkpoint.
//   - The host's own session is stopped when idle. When it is busy and the
//     claim may wait, the host hands back after its turn (lease.Wants). When
//     several devices wait, the most-preferred one gets it (lease.Prefers).
//
// A claim that has to wait returns Wait; the claimant watches the current
// runner's live session meanwhile.
func claimRunner(q Request, paths runnerPaths) (runnerClaim, error) {
	if q.Runner == "" || q.Runner == runnerHub {
		return runnerClaim{}, fmt.Errorf("invalid runner name")
	}
	workspace, err := workspaceInfo(q.ID)
	if err != nil {
		return runnerClaim{}, err
	}
	lease, _, err := loadRunnerLease(paths, q.ID)
	if err != nil {
		return runnerClaim{}, err
	}
	if lease.Owner != "" && lease.Owner != q.Runner && lease.online() && !q.Force {
		if q.Passive {
			return runnerClaim{Wait: true, Runner: lease.Owner}, nil
		}
		if lease = askRunnerToHandOver(paths, q.ID, lease); lease.Owner != "" && lease.Owner != q.Runner && lease.online() {
			if q.Wait {
				return runnerClaim{Wait: true, Runner: lease.Owner}, nil
			}
			return runnerClaim{}, fmt.Errorf("%s is busy with this conversation; it hands over when its turn ends", lease.Owner)
		}
	}
	var claim runnerClaim
	_, err = updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
		if lease.Owner == "" && q.Passive {
			if preferred := lease.yieldsTo(q.Runner); preferred != "" {
				claim = runnerClaim{Wait: true, Runner: runnerHub}
				return nil
			}
		}
		if lease.Owner != "" && lease.Owner != q.Runner {
			if lease.online() && !q.Force {
				claim = runnerClaim{Wait: true, Runner: lease.Owner}
				return nil
			}
			lease.replaceDevice()
		}
		if stopErr := stopIdleChatDaemon(q.ID, true); stopErr != nil {
			if !q.Wait {
				return fmt.Errorf("the remote host is running this workspace's agent: %w", stopErr)
			}
			lease.startWaitRound()
			if _, waiting := lease.Wants[q.Runner]; !waiting {
				lease.Wants[q.Runner] = time.Now().Unix()
			}
			claim = runnerClaim{Wait: true, Runner: runnerHub}
			return nil
		}
		if lease.LastRunner == runnerHub || lease.LastRunner == "" {
			if hubErr := checkpointHubConversations(q.ID, workspace.Path, paths, lease); hubErr != nil {
				fmt.Fprintln(os.Stderr, "Could not checkpoint the remote host's conversations:", hubErr)
			}
		}
		now := time.Now().Unix()
		lease.Owner, lease.Epoch, lease.ClaimedAt, lease.LastSeen, lease.LastRunner = q.Runner, lease.Epoch+1, now, now, q.Runner
		lease.Wants, lease.HandBackAt, lease.Link, lease.Busy, lease.Pending = nil, 0, "", false, false
		claim = runnerClaim{Epoch: lease.Epoch, Seq: lease.Seq, Writer: lease.Writer, Conversation: lease.Conversation,
			Socket: paths.socket(lease.Epoch), Root: workspace.Path}
		return nil
	})
	return claim, err
}

// askRunnerToHandOver sends jara/release through the device's tunnel and
// waits for the release when the device is idle. A busy device remembers the
// request and releases after its turn.
func askRunnerToHandOver(paths runnerPaths, id string, lease runnerLease) runnerLease {
	conn, err := dialRunner(paths, lease)
	if err != nil {
		return lease
	}
	if err = requestRunnerRelease(conn); err != nil || lease.Busy || lease.Pending {
		return lease
	}
	deadline := time.Now().Add(90 * time.Second) // a final checkpoint transfers files
	for time.Now().Before(deadline) {
		current, _, loadErr := loadRunnerLease(paths, id)
		if loadErr == nil && (current.Owner != lease.Owner || current.Epoch != lease.Epoch || current.Busy) {
			return current
		}
		time.Sleep(250 * time.Millisecond)
	}
	current, _, _ := loadRunnerLease(paths, id)
	return current
}

// requestRunnerRelease asks a device's session to save a final checkpoint
// and give up the lease, now or when its turn ends.
func requestRunnerRelease(conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
		return err
	}
	return json.NewEncoder(conn).Encode(map[string]string{"method": "jara/release"})
}
