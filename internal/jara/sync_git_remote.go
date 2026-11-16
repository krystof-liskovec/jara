package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"krystof.liskovec/jara/internal/fsutil"
)

// syncGitRPC serves the commit side of `jara sync`: reporting the checked-out
// commit, bundling commits for a pulling client, and fast-forwarding to
// commits a pushing client sent.
func syncGitRPC(q Request) error {
	switch q.Op {
	case "git-state":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(checkoutState(w.Path))
	case "git-bundle":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		if !commitIDRE.MatchString(q.Base) || !commitIDRE.MatchString(q.Commit) {
			return fmt.Errorf("invalid commit")
		}
		if current := checkoutState(w.Path); current.Head != q.Commit {
			return fmt.Errorf("remote checkout changed during sync; retry")
		}
		if !hasCommit(w.Path, q.Base) || !isAncestor(w.Path, q.Base, q.Commit) {
			return fmt.Errorf("the remote checkout does not build on local commit %s; histories have diverged, so reconcile them with Git", shortCommit(q.Base))
		}
		cmd := exec.Command("git", "-C", w.Path, "bundle", "create", "-", "HEAD", "^"+q.Base)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case "git-advance":
		w, unlock, err := lockWorkspaceForWrite(q)
		if err != nil {
			return err
		}
		defer unlock()
		if !commitIDRE.MatchString(q.Base) || !commitIDRE.MatchString(q.Commit) {
			return fmt.Errorf("invalid commit")
		}
		temp, err := os.CreateTemp(fsutil.DataRoot(), "bundle-*")
		if err != nil {
			return err
		}
		defer os.Remove(temp.Name())
		if _, err = io.Copy(temp, os.Stdin); err != nil {
			temp.Close()
			return err
		}
		if err = temp.Close(); err != nil {
			return err
		}
		return fastForwardCheckout(w.Path, gitCheckoutState{Head: q.Base, Branch: q.Branch}, q.Commit, temp.Name())
	case "git-switch":
		return switchRemoteBranch(q)
	}
	return fmt.Errorf("unknown RPC operation")
}

// lockWorkspaceForWrite takes the workspace lock for an operation that changes
// its files, stopping an idle persistent session first and refusing while an
// agent runs there unless the request forces it.
func lockWorkspaceForWrite(q Request) (Workspace, func(), error) {
	if err := releaseIdleChatDaemon(q.ID); err != nil {
		return Workspace{}, nil, err
	}
	unlock, err := lockWorkspace(q.ID)
	if err != nil {
		return Workspace{}, nil, err
	}
	w, err := workspaceInfo(q.ID)
	if err != nil {
		unlock()
		return Workspace{}, nil, err
	}
	ss, err := activeSessions()
	if err != nil {
		unlock()
		return Workspace{}, nil, err
	}
	if !q.Force {
		for _, s := range ss {
			if s.Workspace == q.ID {
				unlock()
				return Workspace{}, nil, fmt.Errorf("running agent in workspace; detach is insufficient, stop it or explicitly use --force")
			}
		}
	}
	return w, unlock, nil
}
