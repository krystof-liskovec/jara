package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

// A standby keeps a mirror of each workspace its primary forwards: the
// checkout (commits first, then files like `jara sync push`), every
// checkpointed conversation in its own runner store, and the primary's last
// view of the lease. The mirror uses the primary's workspace ID.

// standbyCheckpoint is what a primary forwards besides the checkout.
type standbyCheckpoint struct {
	Checkpoint runnerCheckpoint `json:"checkpoint"`
	Lease      runnerLease      `json:"lease"` // the primary's lease
}

// standbyRecord is the standby's view of a workspace's primary.
type standbyRecord struct {
	Primary    string      `json:"primary"`
	Lease      runnerLease `json:"lease"`
	ReceivedAt int64       `json:"received_at"`
}

type standbyPrepared struct {
	Head   string `json:"head,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Busy means an agent works in the mirror on this host, because you chose
	// to run the workspace here; forwarding waits rather than overwrite it.
	Busy bool `json:"busy,omitempty"`
	// Seq is the standby's checkpoint count; 0 asks for every conversation.
	Seq int64 `json:"seq,omitempty"`
}

func standbyRecordPath(paths runnerPaths) string {
	return filepath.Join(paths.dir, paths.key+".standby.json")
}

func standbyRPC(q Request, body io.Reader) error {
	if !nameRE.MatchString(q.ID) {
		return fmt.Errorf("invalid workspace ID")
	}
	out := json.NewEncoder(os.Stdout)
	switch q.Op {
	case "standby-prepare":
		prepared, err := prepareStandbyWorkspace(q)
		if err != nil {
			return err
		}
		return out.Encode(prepared)
	case "standby-git":
		return applyStandbyCommits(q, body)
	case "standby-checkpoint":
		var forwarded standbyCheckpoint
		if err := decodeMaybeCompressed(body, maxTransfer*2, &forwarded); err != nil {
			return fmt.Errorf("read standby checkpoint: %w", err)
		}
		answer, err := storeStandbyCheckpoint(q, forwarded)
		if err != nil {
			return err
		}
		return out.Encode(answer)
	}
	return fmt.Errorf("unknown RPC operation %s", q.Op)
}

// prepareStandbyWorkspace creates the mirror when it is missing, as a Git
// repository when the primary's is one, and reports its checkout.
func prepareStandbyWorkspace(q Request) (standbyPrepared, error) {
	unlock, err := lockWorkspace(q.ID)
	if err != nil {
		return standbyPrepared{}, err
	}
	defer unlock()
	path, err := workspacePath(q.ID)
	if err != nil {
		return standbyPrepared{}, err
	}
	if _, statErr := os.Stat(path + ".json"); os.IsNotExist(statErr) {
		if err = os.MkdirAll(path, 0700); err != nil {
			return standbyPrepared{}, err
		}
		workspace := Workspace{ID: q.ID, Name: q.Name, Path: path, Branch: q.Branch, Ready: true,
			SourceHost: "standby of " + q.Peer, Created: time.Now().UTC().Format(time.RFC3339)}
		if err = fsutil.WriteJSON(path+".json", workspace); err != nil {
			return standbyPrepared{}, err
		}
	}
	if q.Commit != "" && checkoutState(path).Head == "" {
		if _, statErr := os.Stat(filepath.Join(path, ".git")); os.IsNotExist(statErr) {
			if err = gitRun(path, "init", "--quiet"); err != nil {
				return standbyPrepared{}, err
			}
		}
	}
	state := checkoutState(path)
	prepared := standbyPrepared{Head: state.Head, Branch: state.Branch, Busy: chatDaemonRunning(q.ID)}
	if paths, pathErr := runnerPathSet(q.ID); pathErr == nil {
		if lease, _, leaseErr := loadRunnerLease(paths, q.ID); leaseErr == nil {
			prepared.Seq = lease.Seq
		}
	}
	if sessions, sessionErr := activeSessions(); sessionErr == nil {
		for _, session := range sessions {
			prepared.Busy = prepared.Busy || session.Workspace == q.ID
		}
	}
	return prepared, nil
}

// applyStandbyCommits fetches the primary's commits from a bundle (none when
// only the branch changed) and checks out its branch at its commit. Files
// follow through the ordinary file transfer.
func applyStandbyCommits(q Request, bundle io.Reader) error {
	unlock, err := lockWorkspace(q.ID)
	if err != nil {
		return err
	}
	defer unlock()
	workspace, err := workspaceInfo(q.ID)
	if err != nil {
		return err
	}
	if !commitIDRE.MatchString(q.Commit) || q.Branch != "" && gitRun(workspace.Path, "check-ref-format", "--branch", q.Branch) != nil {
		return fmt.Errorf("invalid commit or branch")
	}
	if !hasCommit(workspace.Path, q.Commit) {
		temp, err := os.CreateTemp(fsutil.DataRoot(), "standby-bundle-*")
		if err != nil {
			return err
		}
		defer os.Remove(temp.Name())
		if _, err = io.Copy(temp, io.LimitReader(bundle, maxTransfer*4)); err != nil {
			temp.Close()
			return err
		}
		if err = temp.Close(); err != nil {
			return err
		}
		if err = gitRun(workspace.Path, "fetch", "--quiet", temp.Name(), "HEAD"); err != nil {
			return err
		}
	}
	if q.Branch == "" {
		return gitRun(workspace.Path, "checkout", "--quiet", "--force", "--detach", q.Commit)
	}
	return gitRun(workspace.Path, "checkout", "--quiet", "--force", "-B", q.Branch, q.Commit)
}

// storeStandbyCheckpoint stores forwarded conversations like a device's
// checkpoint, so the standby can continue them, and records the primary's
// lease.
func storeStandbyCheckpoint(q Request, forwarded standbyCheckpoint) (runnerCheckpointAnswer, error) {
	paths, err := runnerPathSet(q.ID)
	if err != nil {
		return runnerCheckpointAnswer{}, err
	}
	lease, err := updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
		writer := forwarded.Lease.Writer
		if writer == "" {
			writer = q.Peer
		}
		return storeRunnerCheckpoint(paths, lease, forwarded.Checkpoint, writer)
	})
	if resync, ok := err.(*runnerResyncError); ok {
		return runnerCheckpointAnswer{Resync: resync.files}, nil
	}
	if err != nil {
		return runnerCheckpointAnswer{}, err
	}
	record := standbyRecord{Primary: q.Peer, Lease: forwarded.Lease, ReceivedAt: time.Now().Unix()}
	return runnerCheckpointAnswer{Seq: lease.Seq}, fsutil.WriteJSON(standbyRecordPath(paths), record)
}
