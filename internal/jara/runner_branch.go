package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// While a device runs the agent, the remote host's checkout mirrors it, and
// commits only move between checkouts on the same branch. When the branches
// differ, the checkout that changed branch since the last sync leads:
//
//   - The device leads when it has never synchronized, when its branch changed
//     since the last sync (you or its agent switched), and always while it
//     runs the agent. The host switches to the device's branch and keeps its
//     own uncommitted changes in a stash.
//   - The host leads when only it changed branch, because its agent switched
//     while it continued the conversation (the device slept, for example).
//     A clean device then switches to the host's branch.
//
// Commits on the branch left behind stay on that branch.

type runnerBranchMove string

const (
	runnerBranchKept   runnerBranchMove = ""
	runnerBranchHost   runnerBranchMove = "host"
	runnerBranchDevice runnerBranchMove = "device"
)

// alignRunnerBranch puts both checkouts on one branch before files move. last
// is the branch both shared at the device's last sync; deviceLeads forces the
// host to follow. force lets the host switch while an agent session runs there.
func alignRunnerBranch(o localRunnerOpen, last string, deviceLeads, force bool) (runnerBranchMove, error) {
	if !o.isGit {
		return runnerBranchKept, nil
	}
	local := checkoutState(o.root)
	if local.Head == "" {
		return runnerBranchKept, nil
	}
	// The answer stays in o.known for the sync that follows, unless a
	// checkout switches branch below.
	answer, err := o.known.gitState(o.remote, o.binding, local.Head)
	if err != nil {
		return runnerBranchKept, err
	}
	remote := answer.gitCheckoutState
	if remote.Head == "" || remote.Branch == local.Branch {
		return runnerBranchKept, nil
	}
	o.known.forget()
	if !deviceLeads && last != "" && local.Branch == last {
		return runnerBranchDevice, followHostBranch(o.root, remote)
	}
	if local.Branch == "" {
		return runnerBranchKept, fmt.Errorf("this checkout has a detached HEAD; switch to a branch so %s can follow it", o.remoteName)
	}
	var moved struct {
		Stash string `json:"stash"`
	}
	q := Request{Op: "git-switch", ID: o.binding.Workspace, Branch: local.Branch, Commits: branchBaseCandidates(o.root, local, remote), Force: force}
	if err := rpc(o.remote, q, nil, &moved); err != nil {
		if strings.Contains(err.Error(), "unknown RPC operation") {
			err = fmt.Errorf("%s is on %s; upgrade its Jara (`jara deployment upgrade`) so it follows this checkout's branch, or switch one so both match", o.remoteName, remote.describe())
		}
		return runnerBranchKept, err
	}
	fmt.Fprintf(os.Stderr, "%s switched from %s to branch %q to follow this checkout.\n", o.remoteName, remote.describe(), local.Branch)
	if moved.Stash != "" {
		fmt.Fprintf(os.Stderr, "Its uncommitted changes are in a Git stash there (%q).\n", moved.Stash)
	}
	return runnerBranchHost, nil
}

// branchBaseCandidates lists commits the host may already have, newest first,
// so it can start a branch it lacks where this checkout's history meets its
// own: the merge base when this checkout knows the host's commit, then the
// branch's first-parent history.
func branchBaseCandidates(root string, local, remote gitCheckoutState) []string {
	var commits []string
	if hasCommit(root, remote.Head) {
		if base, err := gitOutput(root, "merge-base", local.Head, remote.Head); err == nil && commitIDRE.MatchString(base) {
			commits = append(commits, base)
		}
	}
	history, err := gitOutput(root, "rev-list", "--first-parent", "--max-count=256", local.Head)
	if err == nil {
		commits = append(commits, strings.Fields(history)...)
	}
	return commits
}

// followHostBranch switches this checkout to the branch the host's agent moved
// to. Uncommitted changes come along; Git refuses when they would conflict.
func followHostBranch(root string, remote gitCheckoutState) error {
	if remote.Branch == "" {
		return fmt.Errorf("the remote checkout moved to %s while it ran the agent; switch it to a branch, then retry", remote.describe())
	}
	args := []string{"switch", "--quiet", remote.Branch}
	if gitRun(root, "show-ref", "--verify", "--quiet", "refs/heads/"+remote.Branch) != nil {
		args = []string{"switch", "--quiet", "-c", remote.Branch}
	}
	if err := gitRun(root, args...); err != nil {
		return fmt.Errorf("the remote host switched to branch %q while it ran the agent, and this checkout cannot follow (commit or stash the changes here, then retry): %w", remote.Branch, err)
	}
	fmt.Fprintf(os.Stderr, "Switched this checkout to branch %q, which the remote host moved to while it ran the agent.\n", remote.Branch)
	return nil
}

// switchRemoteBranch serves git-switch on the host: it sets uncommitted
// changes aside in a stash, then checks out the branch, creating it at the
// newest candidate commit it has (or its current commit) when it is missing.
func switchRemoteBranch(q Request) error {
	w, unlock, err := lockWorkspaceForWrite(q)
	if err != nil {
		return err
	}
	defer unlock()
	if q.Branch == "" || strings.HasPrefix(q.Branch, "-") || gitRun(w.Path, "check-ref-format", "--branch", q.Branch) != nil {
		return fmt.Errorf("invalid branch name")
	}
	current := checkoutState(w.Path)
	if current.Head == "" {
		return fmt.Errorf("the remote workspace is not a Git checkout")
	}
	var answer struct {
		Stash string `json:"stash,omitempty"`
	}
	if status, statusErr := gitOutput(w.Path, "status", "--porcelain"); statusErr != nil {
		return statusErr
	} else if status != "" {
		answer.Stash = fmt.Sprintf("jara: work on %s set aside %s", current.describe(), time.Now().Format("2006-01-02 15:04:05"))
		if err = gitRun(w.Path, "stash", "push", "--include-untracked", "--message", answer.Stash); err != nil {
			return err
		}
	}
	if gitRun(w.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+q.Branch) == nil {
		err = gitRun(w.Path, "switch", "--quiet", q.Branch)
	} else {
		start := current.Head
		for _, commit := range q.Commits {
			if commitIDRE.MatchString(commit) && hasCommit(w.Path, commit) {
				start = commit
				break
			}
		}
		err = gitRun(w.Path, "switch", "--quiet", "-c", q.Branch, start)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(answer)
}

// pushBeforeRunning copies this checkout to the host before the agent starts
// here, when the host has nothing newer. A host that just followed this
// checkout's branch set its own changes aside, so the push overwrites it; a
// checkout that followed the host's branch pulls that branch instead.
func pushBeforeRunning(o localRunnerOpen, last string) error {
	moved, err := alignRunnerBranch(o, last, false, false)
	if err != nil {
		return err
	}
	if moved == runnerBranchDevice {
		status, statusErr := gitOutput(o.root, "status", "--porcelain")
		return syncWorkspaceKnown(o.remote, o.binding, o.root, "pull", statusErr == nil && status == "", o.known, o.includeIgnored, false)
	}
	return syncWorkspaceKnown(o.remote, o.binding, o.root, "push", moved == runnerBranchHost, o.known, o.includeIgnored, false)
}
