package jara

import (
	"strings"
	"testing"
)

// Remote work pulled as uncommitted edits, then edited again here and
// committed: the remote copy is only what the last sync mirrored, so a push
// replaces it instead of refusing.
func TestSyncPushReplacesRemoteEditsUnchangedSinceLastSync(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.remote, "a.txt", "a agent draft\n")
	writeSyncFile(t, f.remote, "c.txt", "c agent draft\n")
	if err := f.sync("pull"); err != nil {
		t.Fatal(err)
	}
	writeSyncFile(t, f.local, "a.txt", "a agent draft, edited here\n")
	f.commit(t, f.local, "finish agent work")

	if err := f.sync("push"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.remote) != gitHead(f.local) || syncStatus(t, f.remote) != "" {
		t.Fatalf("remote not converged to the commit: status %q", syncStatus(t, f.remote))
	}
	if readSyncFile(t, f.remote, "a.txt") != "a agent draft, edited here\n" {
		t.Fatal("remote kept the stale mirrored edit")
	}
}

// The reverse: local edits pushed as files, then edited again remotely and
// committed there. The local copy is stale, so a pull replaces it.
func TestSyncPullReplacesLocalEditsUnchangedSinceLastSync(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.local, "b.txt", "b local draft\n")
	if err := f.sync("push"); err != nil {
		t.Fatal(err)
	}
	writeSyncFile(t, f.remote, "b.txt", "b local draft, edited by the agent\n")
	f.commit(t, f.remote, "agent finishes")

	if err := f.sync("pull"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.local) != gitHead(f.remote) || syncStatus(t, f.local) != "" {
		t.Fatalf("local not converged to the commit: status %q", syncStatus(t, f.local))
	}
	if readSyncFile(t, f.local, "b.txt") != "b local draft, edited by the agent\n" {
		t.Fatal("local kept the stale mirrored edit")
	}
}

// A mirrored file edited again on the remote since the last sync holds work
// the local commit lacks, so the push still refuses.
func TestSyncPushKeepsRemoteEditsMadeSinceLastSync(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.remote, "a.txt", "a agent draft\n")
	if err := f.sync("pull"); err != nil {
		t.Fatal(err)
	}
	writeSyncFile(t, f.local, "a.txt", "a agent draft, edited here\n")
	f.commit(t, f.local, "finish agent work")
	before := gitHead(f.remote)
	writeSyncFile(t, f.remote, "a.txt", "a agent draft, more agent work\n")

	err := f.sync("push")
	if err == nil || !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "overwritten") {
		t.Fatalf("remote edit since the last sync was not protected: %v", err)
	}
	if gitHead(f.remote) != before || readSyncFile(t, f.remote, "a.txt") != "a agent draft, more agent work\n" {
		t.Fatal("refused push changed the remote")
	}
}

func TestSyncedSignatureFollowsGitFileSemantics(t *testing.T) {
	big := strings.Repeat("x", maxSyncedRecordBytes)
	if got := syncedSignature(File{Hash: big, Mode: 0644}); got != "f:"+big[:32] {
		t.Fatalf("regular file signature %q", got)
	}
	if got := syncedSignature(File{Hash: "abc", Mode: 0755}); got != "x:abc" {
		t.Fatalf("executable signature %q", got)
	}
	if got := syncedSignature(File{Link: "target"}); got != "l:target" {
		t.Fatalf("link signature %q", got)
	}
}
