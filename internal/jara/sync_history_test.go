package jara

import (
	"errors"
	"strings"
	"testing"
)

// After a history rewrite the push refuses; replacing the remote history keeps
// the remote's old commit and uncommitted work, then file sync carries on.
func TestSyncPushReplacesRewrittenRemoteHistory(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.local, "a.txt", "a shared\n")
	f.commit(t, f.local, "shared work")
	if err := f.sync("push"); err != nil {
		t.Fatal(err)
	}
	old := gitHead(f.remote)
	writeSyncFile(t, f.remote, "b.txt", "b remote draft\n")
	writeSyncFile(t, f.remote, "scratch.txt", "remote untracked\n")

	// Rewrite the last commit, as an amend or a force-pushed branch would.
	writeSyncFile(t, f.local, "a.txt", "a rewritten\n")
	gitSyncTestCommand(t, f.local, "commit", "-q", "-a", "--amend", "-m", "shared work, rewritten")
	writeSyncFile(t, f.local, "c.txt", "c uncommitted\n")

	err := f.sync("push")
	var diverged *divergedHistoryError
	if !errors.As(err, &diverged) {
		t.Fatalf("diverged history was not reported as such: %v", err)
	}
	if diverged.RemoteOnly != 1 || len(diverged.RemoteSubjects) != 1 || !strings.HasSuffix(diverged.RemoteSubjects[0], " shared work") {
		t.Fatalf("remote-only commits misreported: %+v", diverged)
	}
	if gitHead(f.remote) != old {
		t.Fatal("refused push moved the remote checkout")
	}

	if err = replaceRemoteHistory(f.remoteHost, f.binding, f.local, diverged.Local, diverged.Remote); err != nil {
		t.Fatal(err)
	}
	if err = f.sync("push"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.remote) != gitHead(f.local) {
		t.Fatal("remote checkout was not moved to the local history")
	}
	if readSyncFile(t, f.remote, "a.txt") != "a rewritten\n" || readSyncFile(t, f.remote, "c.txt") != "c uncommitted\n" {
		t.Fatal("remote files do not match local after the replacement")
	}
	if status := syncStatus(t, f.remote); status != "?? c.txt" {
		t.Fatalf("remote should show only the local uncommitted file, got %q", status)
	}
	backups := strings.Fields(string(gitSyncTestCommand(t, f.remote, "for-each-ref", "--format=%(objectname)", "refs/heads/jara/replaced/")))
	if len(backups) != 1 || backups[0] != old {
		t.Fatalf("old remote commit not kept on a backup branch: %v", backups)
	}
	stash := string(gitSyncTestCommand(t, f.remote, "stash", "show", "--include-untracked", "--name-only"))
	if !strings.Contains(stash, "b.txt") || !strings.Contains(stash, "scratch.txt") {
		t.Fatalf("remote uncommitted work not stashed: %q", stash)
	}
}

// The replacement refuses when the remote checkout moved since it was read.
func TestReplaceRemoteHistoryRefusesWhenRemoteMoved(t *testing.T) {
	f := newCommitSyncFixture(t)
	stale := checkoutState(f.remote)
	writeSyncFile(t, f.local, "a.txt", "a rewritten\n")
	gitSyncTestCommand(t, f.local, "commit", "-q", "-a", "--amend", "-m", "base, rewritten")
	writeSyncFile(t, f.remote, "d.txt", "remote commit\n")
	f.commit(t, f.remote, "remote work")
	moved := gitHead(f.remote)

	err := replaceRemoteHistory(f.remoteHost, f.binding, f.local, checkoutState(f.local), stale)
	if err == nil || !strings.Contains(err.Error(), "changed during sync") {
		t.Fatalf("moved remote checkout was not detected: %v", err)
	}
	if gitHead(f.remote) != moved {
		t.Fatal("refused replacement changed the remote checkout")
	}
}

// rewriteRemoteHistory amends the remote's last commit, as an agent's rebase
// there would, leaving a commit this checkout has never seen.
func rewriteRemoteHistory(t *testing.T, f commitSyncFixture) {
	t.Helper()
	writeSyncFile(t, f.remote, "a.txt", "a rewritten on the host\n")
	gitSyncTestCommand(t, f.remote, "commit", "-q", "-a", "--amend", "-m", "base, rewritten on the host")
	writeSyncFile(t, f.remote, "b.txt", "b host draft\n")
}

// A host history this checkout never saw is reported as diverged in both
// directions instead of as newer host commits.
func TestSyncReportsUnknownRewrittenRemoteHistoryAsDiverged(t *testing.T) {
	f := newCommitSyncFixture(t)
	rewriteRemoteHistory(t, f)
	if hasCommit(f.local, gitHead(f.remote)) {
		t.Fatal("fixture: local unexpectedly has the rewritten commit")
	}
	for _, direction := range []string{"push", "pull"} {
		var diverged *divergedHistoryError
		if err := f.sync(direction); !errors.As(err, &diverged) {
			t.Fatalf("%s: rewritten remote history was not reported as diverged: %v", direction, err)
		}
	}
	// Newer host commits on the same history still ask for a pull.
	gitSyncTestCommand(t, f.remote, "reset", "-q", "--hard", gitHead(f.local))
	writeSyncFile(t, f.remote, "d.txt", "host commit\n")
	f.commit(t, f.remote, "host work")
	if err := f.sync("push"); err == nil || !strings.Contains(err.Error(), "jara sync pull") {
		t.Fatalf("newer host commits were not reported: %v", err)
	}
}

// Keeping the host's history moves this checkout onto commits it fetches
// from the host, keeps its old state, then pulls the host's edits.
func TestSyncPullMovesLocalCheckoutOntoRemoteHistory(t *testing.T) {
	f := newCommitSyncFixture(t)
	old := gitHead(f.local)
	rewriteRemoteHistory(t, f)
	writeSyncFile(t, f.local, "notes.txt", "local untracked\n")

	err := f.sync("pull")
	var diverged *divergedHistoryError
	if !errors.As(err, &diverged) {
		t.Fatalf("diverged history was not reported: %v", err)
	}
	if err = moveLocalHistory(f.remoteHost, f.binding, f.local, diverged.Local, diverged.Remote); err != nil {
		t.Fatal(err)
	}
	if err = f.sync("pull"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.local) != gitHead(f.remote) {
		t.Fatal("local checkout was not moved to the remote history")
	}
	if readSyncFile(t, f.local, "a.txt") != "a rewritten on the host\n" || readSyncFile(t, f.local, "b.txt") != "b host draft\n" {
		t.Fatal("local files do not match the remote after the move")
	}
	backups := strings.Fields(string(gitSyncTestCommand(t, f.local, "for-each-ref", "--format=%(objectname)", "refs/heads/jara/replaced/")))
	if len(backups) != 1 || backups[0] != old {
		t.Fatalf("old local commit not kept on a backup branch: %v", backups)
	}
	if stash := string(gitSyncTestCommand(t, f.local, "stash", "show", "--include-untracked", "--name-only")); !strings.Contains(stash, "notes.txt") {
		t.Fatalf("local uncommitted work not stashed: %q", stash)
	}
}

// Keeping this checkout's history works without the host's commit here.
func TestReplaceUnknownRemoteHistory(t *testing.T) {
	f := newCommitSyncFixture(t)
	rewriteRemoteHistory(t, f)
	var diverged *divergedHistoryError
	if err := f.sync("push"); !errors.As(err, &diverged) {
		t.Fatalf("diverged history was not reported: %v", err)
	}
	if err := replaceRemoteHistory(f.remoteHost, f.binding, f.local, diverged.Local, diverged.Remote); err != nil {
		t.Fatal(err)
	}
	if err := f.sync("push"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.remote) != gitHead(f.local) || syncStatus(t, f.remote) != "" {
		t.Fatalf("remote not moved to the local history: status %q", syncStatus(t, f.remote))
	}
}
