package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// setAsideLocalWork preserves work in this checkout that a newer checkpoint
// from another device would overwrite, typically edits made after this
// device's last checkpoint before another device took over. Git checkouts
// keep uncommitted work in a stash and unsynced commits on a branch; copied
// folders keep changed files under Jara's data directory.
func setAsideLocalWork(o localRunnerOpen, id string) error {
	summary, err := localPushChangesDetails(o.binding, o.root, o.includeIgnored, o.isGit)
	if err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	if !o.isGit {
		if !summary.Pending || len(summary.Files) == 0 {
			return nil
		}
		dest := filepath.Join(fsutil.DataRoot(), "set-aside", id, stamp)
		for _, name := range summary.Files {
			if err = copySetAsideFile(o.root, dest, name); err != nil {
				return fmt.Errorf("set aside %s: %w", name, err)
			}
		}
		fmt.Fprintf(os.Stderr, "Set aside %d locally changed files in %s.\n", len(summary.Files), dest)
		return nil
	}
	// Edits already in the last checkpoint need no stash; the pull updates them.
	if status, _ := gitOutput(o.root, "status", "--porcelain"); status != "" && summary.Pending {
		message := "jara: local work set aside " + stamp + " before continuing from the remote"
		if err = runGit(o.root, "stash", "push", "--include-untracked", "--message", message); err != nil {
			return fmt.Errorf("set aside uncommitted work: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Set aside uncommitted work in a Git stash (%q).\n", message)
	}
	var remote gitCheckoutState
	if err = rpc(o.remote, Request{Op: "git-state", ID: o.binding.Workspace}, nil, &remote); err != nil {
		return err
	}
	local := checkoutState(o.root)
	if local.Head == "" || remote.Head == "" || local.Head == remote.Head {
		return nil
	}
	if hasCommit(o.root, remote.Head) && isAncestor(o.root, local.Head, remote.Head) {
		return nil // only behind; the pull fast-forwards
	}
	base := syncedLocalHead(o.binding, o.root)
	if base == "" || !hasCommit(o.root, base) || !isAncestor(o.root, base, local.Head) {
		return fmt.Errorf("this checkout has commits the remote copy lacks and no shared starting point is recorded; " +
			"move them to another branch and reset this one to the remote's commit, then retry")
	}
	branch := "jara/set-aside-" + stamp
	if err = runGit(o.root, "branch", branch, local.Head); err != nil {
		return fmt.Errorf("keep unsynced commits: %w", err)
	}
	if err = runGit(o.root, "reset", "--hard", base); err != nil {
		return fmt.Errorf("rewind to the last synced commit: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Moved commits the remote copy lacks to branch %s.\n", branch)
	return nil
}

// syncedLocalHead is the local commit recorded at the last successful sync.
func syncedLocalHead(b Binding, root string) string {
	path, err := syncStatePath(b, root)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var baseline Baseline
	if json.Unmarshal(data, &baseline) != nil {
		return ""
	}
	return baseline.LocalHead
}

func copySetAsideFile(root, dest, name string) error {
	source := filepath.Join(root, filepath.FromSlash(name))
	target := filepath.Join(dest, filepath.FromSlash(name))
	if !pathWithin(root, source) || !pathWithin(dest, target) {
		return fmt.Errorf("path escapes the workspace")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(link, target)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, info.Mode().Perm()|0600)
}
