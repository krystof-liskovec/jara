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

	"krystof.liskovec/jara/internal/fsutil"
)

// A runner lease records which machine runs a workspace's agent. The remote
// host keeps it with the workspace, the checkpointed conversations, and the
// tunnel socket that reaches a device's live session.
//
// Owner is empty while no device holds the workspace; the host then runs the
// agent itself. Epoch increases with every change of runner, so a device
// that was replaced (for example, while it slept) cannot write checkpoints or
// open a tunnel any more. A device's link renews LastSeen every few seconds;
// a device that stops renewing it is offline.
type runnerLease struct {
	Workspace string `json:"workspace"`
	Owner     string `json:"owner,omitempty"`
	Epoch     int64  `json:"epoch"`
	ClaimedAt int64  `json:"claimed_at,omitempty"`
	LastSeen  int64  `json:"last_seen,omitempty"`
	// Link identifies the device's current link; an older link that ends
	// after a newer one started must not act.
	Link string `json:"link,omitempty"`
	// Busy and Pending are the device's last report: a turn is running, or
	// scheduled work is waiting. Either makes the host continue for it.
	Busy    bool `json:"busy,omitempty"`
	Pending bool `json:"pending,omitempty"`
	// Want names a device waiting for the current runner to become idle.
	Want string `json:"want,omitempty"`
	// LastRunner is the device or runnerHub that ran the agent most recently.
	LastRunner string `json:"last_runner,omitempty"`
	// Seq numbers checkpoints; Writer wrote checkpoint Seq. Conversation is
	// the conversation the runner had open.
	Seq          int64  `json:"seq,omitempty"`
	Writer       string `json:"writer,omitempty"`
	CheckpointAt int64  `json:"checkpoint_at,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	// Threads records the checkpoint that last changed each conversation.
	Threads map[string]int64 `json:"threads,omitempty"`
	// HubSeq is the checkpoint the host's own agent state reflects, and
	// HubVersions the host's conversation versions at that point.
	HubSeq      int64             `json:"hub_seq,omitempty"`
	HubVersions map[string]string `json:"hub_versions,omitempty"`
}

// runnerHub names the remote host itself as a runner.
const runnerHub = "remote host"

const runnerLostMessage = "runner lease lost"

var errRunnerLost = errors.New(runnerLostMessage + ": another machine continued this conversation")

// runnerHeartbeatInterval is how often a device renews its lease: often
// enough that a few lost heartbeats do not make it look offline.
func runnerHeartbeatInterval() time.Duration {
	return min(5*time.Second, runnerLinkTimeout()/4)
}

// runnerLinkTimeout is how long a device may stay silent before the host
// treats it as offline. JARA_RUNNER_TIMEOUT (seconds) shortens it for tests.
func runnerLinkTimeout() time.Duration {
	if seconds, err := strconv.Atoi(os.Getenv("JARA_RUNNER_TIMEOUT")); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 30 * time.Second
}

type runnerPaths struct{ lease, lock, threads, dir, key string }

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
		threads: filepath.Join(dir, id+".threads"), dir: dir, key: key}, nil
}

// socket is where a device's tunnel exposes its session for one lease epoch.
// The name is short because Unix socket paths are limited to ~100 bytes.
func (p runnerPaths) socket(epoch int64) string {
	return filepath.Join(p.dir, p.key+"-"+strconv.FormatInt(epoch, 10)+".sock")
}

func (p runnerPaths) thread(id string) string { return filepath.Join(p.threads, id+".json") }

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

// runnerLeaseExists reports whether devices have ever run this workspace.
func runnerLeaseExists(id string) bool {
	paths, err := runnerPathSet(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(paths.lease)
	return err == nil
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
		time.Sleep(50 * time.Millisecond)
	}
}

// updateRunnerLease applies change under the lease lock.
func updateRunnerLease(paths runnerPaths, id string, change func(*runnerLease) error) (runnerLease, error) {
	unlock, err := lockRunner(paths)
	if err != nil {
		return runnerLease{}, err
	}
	defer unlock()
	lease, _, err := loadRunnerLease(paths, id)
	if err != nil {
		return runnerLease{}, err
	}
	if err = change(&lease); err != nil {
		return lease, err
	}
	return lease, saveRunnerLease(paths, lease)
}

// online reports whether the device holding the lease renewed it recently.
func (lease runnerLease) online() bool {
	return lease.Owner != "" && time.Since(time.Unix(lease.LastSeen, 0)) < runnerLinkTimeout()
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

// replaceDevice moves the lease off an offline device to the host.
func (lease *runnerLease) replaceDevice() {
	fmt.Fprintf(os.Stderr, "%s is offline; continuing here from its checkpoint of %s.\n", lease.Owner, describeRunnerTime(lease.CheckpointAt))
	lease.Owner, lease.Epoch, lease.Link, lease.Busy, lease.Pending = "", lease.Epoch+1, "", false, false
}

func describeRunnerTime(unix int64) string {
	if unix == 0 {
		return "never"
	}
	return time.Since(time.Unix(unix, 0)).Round(time.Second).String() + " ago"
}
