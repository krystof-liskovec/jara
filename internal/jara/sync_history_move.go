package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// historyMove reports where a moved checkout left its previous state.
type historyMove struct {
	Backup string `json:"backup"`
	Stash  string `json:"stash,omitempty"`
}

// replaceRemoteHistory sends this checkout's commits and resets the remote
// checkout to them, then records the new commit as the shared sync baseline so
// only uncommitted local edits transfer as files afterwards.
func replaceRemoteHistory(r Remote, b Binding, root string, local, remote gitCheckoutState) error {
	var exclude []string
	if hasCommit(root, remote.Head) {
		if base, err := gitOutput(root, "merge-base", local.Head, remote.Head); err == nil && commitIDRE.MatchString(base) {
			exclude = append(exclude, base)
		}
	}
	bundle, cleanup, err := repositoryBundle(root, exclude...)
	if err != nil {
		return err
	}
	defer cleanup()
	var moved historyMove
	q := Request{Op: "git-replace", ID: b.Workspace, Base: remote.Head, Branch: remote.Branch, Commit: local.Head}
	if err = rpc(r, q, bundle, &moved); err != nil {
		if strings.Contains(err.Error(), "unknown RPC operation") {
			return fmt.Errorf("the remote Jara cannot replace checkout history yet; upgrade it with `jara deployment upgrade`, or reset the remote checkout with Git (nothing transferred)")
		}
		return fmt.Errorf("the remote checkout was not moved: %w", err)
	}
	reportHistoryMove("the remote checkout", local.Head, moved, " there")
	return resetSyncBaseline(b, root, local.Head)
}

// moveLocalHistory fetches the remote checkout's commits when this checkout
// lacks them and resets this checkout to the remote commit, keeping its
// previous commit on a backup branch and its uncommitted changes in a stash.
func moveLocalHistory(r Remote, b Binding, root string, local, remote gitCheckoutState) error {
	if current := checkoutState(root); current != local {
		return fmt.Errorf("checkout changed during sync (now %s at %s); retry", current.describe(), shortCommit(current.Head))
	}
	if !hasCommit(root, remote.Head) {
		if err := fetchRemoteHistory(r, b, root, local, remote); err != nil {
			return err
		}
	}
	moved, err := setAsideCheckout(root, local, remote.Head)
	if err != nil {
		return err
	}
	reportHistoryMove("this checkout", remote.Head, moved, "")
	return resetSyncBaseline(b, root, remote.Head)
}

// fetchRemoteHistory brings the remote commit into this repository, sending
// the commits this checkout has so the host leaves out what they share.
func fetchRemoteHistory(r Remote, b Binding, root string, local, remote gitCheckoutState) error {
	have, _ := gitOutput(root, "rev-list", fmt.Sprintf("--max-count=%d", maxBundleExclusions), local.Head)
	temp, err := os.CreateTemp("", "jara-bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	q := Request{Op: "git-bundle", ID: b.Workspace, Commit: remote.Head, Commits: strings.Fields(have)}
	err = rpcStream(r, q, nil, temp)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		if strings.Contains(err.Error(), "invalid commit") {
			err = fmt.Errorf("%w; upgrade the remote Jara with `jara deployment upgrade`", err)
		}
		return fmt.Errorf("commits were not transferred: %w", err)
	}
	if err = gitRun(root, "fetch", "--quiet", temp.Name(), "HEAD"); err != nil {
		return err
	}
	if !hasCommit(root, remote.Head) {
		return fmt.Errorf("commit %s was not transferred; retry", shortCommit(remote.Head))
	}
	return nil
}

func reportHistoryMove(what, commit string, moved historyMove, where string) {
	fmt.Fprintf(os.Stderr, "Moved %s to %s; its previous commit is on branch %q%s.\n", what, shortCommit(commit), moved.Backup, where)
	if moved.Stash != "" {
		fmt.Fprintf(os.Stderr, "Its uncommitted changes are in a Git stash%s (%q).\n", where, moved.Stash)
	}
}

// resetSyncBaseline records commit as what both sides last shared, as after
// a fresh clone: edits on top of it are what file sync transfers next.
func resetSyncBaseline(b Binding, root, commit string) error {
	state, err := syncStatePath(b, root)
	if err != nil {
		return err
	}
	committed, err := committedManifest(root, commit)
	if err != nil {
		return err
	}
	return fsutil.WriteJSON(state, Baseline{Local: committed, Remote: committed, LocalHead: commit})
}

// setAsideCheckout keeps the checkout's commit on a new backup branch,
// stashes its uncommitted changes, and resets it to target.
func setAsideCheckout(path string, current gitCheckoutState, target string) (historyMove, error) {
	now := time.Now()
	name := current.Branch
	if name == "" {
		name = "detached"
	}
	moved := historyMove{Backup: "jara/replaced/" + name + "-" + now.Format("20060102-150405")}
	if err := gitRun(path, "branch", "--", moved.Backup, current.Head); err != nil {
		return historyMove{}, err
	}
	if status, err := gitOutput(path, "status", "--porcelain"); err != nil {
		return historyMove{}, err
	} else if status != "" {
		moved.Stash = fmt.Sprintf("jara: work on %s at %s set aside %s before a history replacement", current.describe(), shortCommit(current.Head), now.Format("2006-01-02 15:04:05"))
		if err = gitRun(path, "stash", "push", "--include-untracked", "--message", moved.Stash); err != nil {
			return historyMove{}, err
		}
	}
	return moved, gitRun(path, "reset", "--hard", "--quiet", target)
}

// replaceRemoteCheckout serves git-replace on the host: it moves the checkout
// to the commit the client sent, as setAsideCheckout does. It changes nothing
// when the checkout moved since the client looked.
func replaceRemoteCheckout(q Request) error {
	w, unlock, err := lockWorkspaceForWrite(q)
	if err != nil {
		return err
	}
	defer unlock()
	if !commitIDRE.MatchString(q.Base) || !commitIDRE.MatchString(q.Commit) {
		return fmt.Errorf("invalid commit")
	}
	expected := gitCheckoutState{Head: q.Base, Branch: q.Branch}
	if current := checkoutState(w.Path); current != expected {
		return fmt.Errorf("remote checkout changed during sync (now %s at %s); retry", current.describe(), shortCommit(current.Head))
	}
	bundle, cleanup, err := receiveBundle()
	if err != nil {
		return err
	}
	defer cleanup()
	if err = gitRun(w.Path, "fetch", "--quiet", bundle, "HEAD"); err != nil {
		return err
	}
	if !hasCommit(w.Path, q.Commit) {
		return fmt.Errorf("commit %s was not transferred; retry", shortCommit(q.Commit))
	}
	moved, err := setAsideCheckout(w.Path, expected, q.Commit)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(moved)
}
