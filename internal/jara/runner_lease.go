package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

// A runner lease records which machine runs a workspace's agent. The remote
// host keeps it with the workspace, the latest checkpoint of the running
// conversation, and the tunnel socket that reaches a device's live session.
//
// Owner is empty while no device holds the workspace; the host may then run
// the agent itself. Epoch increases with every claim, so a device that lost
// its lease (for example, after a forced takeover while it was offline)
// cannot write checkpoints or open a tunnel any more.
type runnerLease struct {
	Workspace string `json:"workspace"`
	Owner     string `json:"owner,omitempty"`
	Epoch     int64  `json:"epoch"`
	ClaimedAt int64  `json:"claimed_at,omitempty"`
	LastSeen  int64  `json:"last_seen,omitempty"`
	// LastRunner is the device or runnerHub that ran the agent most recently.
	LastRunner string `json:"last_runner,omitempty"`
	// Seq numbers checkpoints; Writer wrote checkpoint Seq.
	Seq          int64  `json:"seq,omitempty"`
	Writer       string `json:"writer,omitempty"`
	CheckpointAt int64  `json:"checkpoint_at,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	// HubSeq is the checkpoint the host's own agent state reflects.
	HubSeq int64 `json:"hub_seq,omitempty"`
}

// runnerHub names the remote host itself as a runner.
const runnerHub = "remote host"

const runnerLostMessage = "runner lease lost"

var errRunnerLost = errors.New(runnerLostMessage + ": another device continued this conversation")

type runnerPaths struct{ lease, lock, bundle, dir, key string }

func runnerPathSet(id string) (runnerPaths, error) {
	if !nameRE.MatchString(id) {
		return runnerPaths{}, fmt.Errorf("invalid workspace ID")
	}
	dir := filepath.Join(fsutil.DataRoot(), "runners")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return runnerPaths{}, err
	}
	sum := sha256.Sum256([]byte(id))
	key := hex.EncodeToString(sum[:6])
	return runnerPaths{lease: filepath.Join(dir, id+".json"), lock: filepath.Join(dir, id+".lock"),
		bundle: filepath.Join(dir, id+".checkpoint.json"), dir: dir, key: key}, nil
}

// socket is where a device's tunnel exposes its session for one lease epoch.
// The name is short because Unix socket paths are limited to ~100 bytes.
func (p runnerPaths) socket(epoch int64) string {
	return filepath.Join(p.dir, p.key+"-"+strconv.FormatInt(epoch, 10)+".sock")
}

func loadRunnerLease(paths runnerPaths, id string) (runnerLease, bool, error) {
	data, err := os.ReadFile(paths.lease)
	if os.IsNotExist(err) {
		return runnerLease{Workspace: id}, false, nil
	}
	if err != nil {
		return runnerLease{}, false, err
	}
	var lease runnerLease
	if err = json.Unmarshal(data, &lease); err != nil || lease.Workspace != id {
		return runnerLease{}, false, fmt.Errorf("runner lease for %s is unreadable", id)
	}
	return lease, true, nil
}

func saveRunnerLease(paths runnerPaths, lease runnerLease) error {
	return fsutil.WriteJSON(paths.lease, lease)
}

// lockRunner serializes lease changes. Lease operations are short, so a lock
// older than two minutes belongs to a process that died.
func lockRunner(paths runnerPaths) (func(), error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := os.Mkdir(paths.lock, 0700)
		if err == nil {
			return func() { _ = os.Remove(paths.lock) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(paths.lock); statErr == nil && time.Since(info.ModTime()) > 2*time.Minute {
			_ = os.Remove(paths.lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another runner change is in progress for this workspace; retry shortly")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// dialRunner connects to the live session of the device holding the lease.
func dialRunner(paths runnerPaths, lease runnerLease) (net.Conn, error) {
	if lease.Owner == "" {
		return nil, fmt.Errorf("no device runs this workspace")
	}
	return net.DialTimeout("unix", paths.socket(lease.Epoch), 2*time.Second)
}

func (lease runnerLease) holds(runner string, epoch int64) bool {
	return lease.Owner != "" && lease.Owner == runner && lease.Epoch == epoch
}

func describeRunnerTime(unix int64) string {
	if unix == 0 {
		return "never"
	}
	age := time.Since(time.Unix(unix, 0)).Round(time.Second)
	return age.String() + " ago"
}

// offlineRunnerError explains how to continue while the device is offline.
func offlineRunnerError(lease runnerLease) error {
	return fmt.Errorf("this conversation runs on %s, which is not reachable (last seen %s; last checkpoint %s). "+
		"Continue here from that checkpoint with --take-over; work %s did after it is set aside when it reconnects",
		lease.Owner, describeRunnerTime(lease.LastSeen), describeRunnerTime(lease.CheckpointAt), lease.Owner)
}
