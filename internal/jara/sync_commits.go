package jara

import (
	"fmt"
	"os"
	"strings"
)

// commitAdvance records a fast-forward performed by syncCommits.
type commitAdvance struct{ From, To string }

// syncCommits transfers commits before file sync, so committed work arrives
// as commits instead of as uncommitted edits. It only fast-forwards the
// destination and does nothing when either side is not a Git checkout or both
// already share HEAD.
func syncCommits(r Remote, b Binding, root, direction string, force bool) (*commitAdvance, error) {
	local := checkoutState(root)
	if local.Head == "" {
		return nil, nil
	}
	var answer gitStateAnswer
	if err := rpc(r, Request{Op: "git-state", ID: b.Workspace, Commit: local.Head}, nil, &answer); err != nil {
		if !strings.Contains(err.Error(), "unknown RPC operation") {
			return nil, err
		}
		fmt.Fprintln(os.Stderr, "The remote Jara cannot transfer commits yet, so committed changes sync as files; upgrade it with `jara deployment upgrade`.")
		return nil, nil
	}
	remote := answer.gitCheckoutState
	if remote.Head == "" || remote.Head == local.Head {
		return nil, nil
	}
	if remote.Branch != local.Branch {
		return nil, fmt.Errorf("this checkout is on %s but the remote checkout is on %s; switch one so both match, then retry (nothing transferred)", local.describe(), remote.describe())
	}
	if diverged(root, local, remote, answer.Contains) {
		return nil, newDivergedHistoryError(root, local, remote)
	}
	if direction == "push" {
		return pushCommits(r, b, root, local, remote, force)
	}
	return pullCommits(r, b, root, local, remote, force)
}

// diverged reports whether neither checkout's commit builds on the other's.
// When this checkout lacks the remote commit, the remote says whether its
// commit builds on the local one (remoteContainsLocal); a host too old to say
// is assumed to be ahead, which a pull then checks.
func diverged(root string, local, remote gitCheckoutState, remoteContainsLocal *bool) bool {
	if !hasCommit(root, remote.Head) {
		return remoteContainsLocal != nil && !*remoteContainsLocal
	}
	return !isAncestor(root, remote.Head, local.Head) && !isAncestor(root, local.Head, remote.Head)
}

func pushCommits(r Remote, b Binding, root string, local, remote gitCheckoutState, force bool) (*commitAdvance, error) {
	if !hasCommit(root, remote.Head) || isAncestor(root, local.Head, remote.Head) {
		return nil, fmt.Errorf("the remote checkout has commits this checkout lacks; run `jara sync pull` first (nothing transferred)")
	}
	bundle, cleanup, err := repositoryBundle(root, remote.Head)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	q := Request{Op: "git-advance", ID: b.Workspace, Base: remote.Head, Branch: remote.Branch, Commit: local.Head, Force: force}
	if err = rpc(r, q, bundle, nil); err != nil {
		return nil, fmt.Errorf("commits were not transferred: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Fast-forwarded the remote checkout to %s (%s).\n", shortCommit(local.Head), commitCount(root, remote.Head, local.Head))
	return &commitAdvance{From: remote.Head, To: local.Head}, nil
}

func pullCommits(r Remote, b Binding, root string, local, remote gitCheckoutState, force bool) (*commitAdvance, error) {
	known := hasCommit(root, remote.Head)
	if known && isAncestor(root, remote.Head, local.Head) {
		return nil, fmt.Errorf("this checkout has commits the remote checkout lacks; run `jara sync push` first (nothing transferred)")
	}
	// Pull is prohibited while Jara-managed agents are active remotely.
	if err := rpc(r, Request{Op: "guard", ID: b.Workspace, Force: force}, nil, nil); err != nil {
		return nil, err
	}
	bundlePath := ""
	if !known {
		temp, err := os.CreateTemp("", "jara-bundle-*")
		if err != nil {
			return nil, err
		}
		bundlePath = temp.Name()
		defer os.Remove(bundlePath)
		err = rpcStream(r, Request{Op: "git-bundle", ID: b.Workspace, Base: local.Head, Commit: remote.Head}, nil, temp)
		if closeErr := temp.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, fmt.Errorf("commits were not transferred: %w", err)
		}
	}
	if err := fastForwardCheckout(root, local, remote.Head, bundlePath); err != nil {
		return nil, fmt.Errorf("commits were not applied here: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Fast-forwarded this checkout to %s (%s).\n", shortCommit(remote.Head), commitCount(root, local.Head, remote.Head))
	return &commitAdvance{From: local.Head, To: remote.Head}, nil
}

func commitCount(root, from, to string) string {
	count, err := gitOutput(root, "rev-list", "--count", from+".."+to)
	if err != nil {
		return "new commits"
	}
	if count == "1" {
		return "1 commit"
	}
	return count + " commits"
}

// alignCommittedBaseline records the fast-forwarded commit as the shared state
// of every path it touched, so file sync transfers only uncommitted edits made
// on top of it and does not mistake the checkout update for a conflict.
func alignCommittedBaseline(root string, baseline *Baseline, advance *commitAdvance) error {
	paths, err := commitPathsChanged(root, advance.From, advance.To)
	if err != nil {
		return err
	}
	committed, err := committedManifest(root, advance.To)
	if err != nil {
		return err
	}
	if baseline.Local == nil {
		baseline.Local = Snapshot{}
	}
	if baseline.Remote == nil {
		baseline.Remote = Snapshot{}
	}
	for _, name := range paths {
		if f, ok := committed[name]; ok {
			baseline.Local[name] = f
			baseline.Remote[name] = f
		} else {
			delete(baseline.Local, name)
			delete(baseline.Remote, name)
		}
	}
	return nil
}
