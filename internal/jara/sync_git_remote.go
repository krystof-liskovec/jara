package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"krystof.liskovec/jara/internal/fsutil"
)

// gitStateAnswer is the host's checkout state. Contains, when the client named
// its commit, tells whether the host's commit builds on it, so a client that
// lacks the host's commit can tell newer host commits from a diverged history.
// Hosts before it omit Contains.
type gitStateAnswer struct {
	gitCheckoutState
	Contains *bool `json:"contains_commit,omitempty"`
}

// maxBundleExclusions caps the commits a client may list as already present.
const maxBundleExclusions = 512

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
		answer := gitStateAnswer{gitCheckoutState: checkoutState(w.Path)}
		if answer.Head != "" && commitIDRE.MatchString(q.Commit) {
			contains := hasCommit(w.Path, q.Commit) && isAncestor(w.Path, q.Commit, answer.Head)
			answer.Contains = &contains
		}
		return json.NewEncoder(os.Stdout).Encode(answer)
	case "git-bundle":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		if !commitIDRE.MatchString(q.Commit) || (q.Base != "" && !commitIDRE.MatchString(q.Base)) {
			return fmt.Errorf("invalid commit")
		}
		if current := checkoutState(w.Path); current.Head != q.Commit {
			return fmt.Errorf("remote checkout changed during sync; retry")
		}
		args := []string{"-C", w.Path, "bundle", "create", "-", "HEAD"}
		if q.Base != "" {
			if !hasCommit(w.Path, q.Base) || !isAncestor(w.Path, q.Base, q.Commit) {
				return fmt.Errorf("the remote checkout does not build on local commit %s; histories have diverged, so reconcile them with Git", shortCommit(q.Base))
			}
			args = append(args, "^"+q.Base)
		} else {
			// Without a shared base (the client is moving onto this
			// history), leave out what the client says it already has.
			for i, commit := range q.Commits {
				if i < maxBundleExclusions && commitIDRE.MatchString(commit) && hasCommit(w.Path, commit) {
					args = append(args, "^"+commit)
				}
			}
		}
		cmd := exec.Command("git", args...)
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
		bundle, cleanup, err := receiveBundle()
		if err != nil {
			return err
		}
		defer cleanup()
		return fastForwardCheckout(w.Path, gitCheckoutState{Head: q.Base, Branch: q.Branch}, q.Commit, bundle)
	case "git-switch":
		return switchRemoteBranch(q)
	case "git-replace":
		return replaceRemoteCheckout(q)
	}
	return fmt.Errorf("unknown RPC operation")
}

// receiveBundle saves the Git bundle a client sent on stdin to a temporary
// file, returning its path and a function that removes it.
func receiveBundle() (string, func(), error) {
	temp, err := os.CreateTemp(fsutil.DataRoot(), "bundle-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.Remove(temp.Name()) }
	if _, err = io.Copy(temp, os.Stdin); err != nil {
		temp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err = temp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return temp.Name(), cleanup, nil
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
