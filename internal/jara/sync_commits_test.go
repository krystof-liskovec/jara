package jara

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// serveSyncRPCIfRequested lets the fake SSH below run this test binary as the
// remote `jara _rpc` endpoint.
func serveSyncRPCIfRequested() {
	raw := os.Getenv("JARA_SYNC_RPC_TOKEN")
	if raw == "" {
		return
	}
	var q Request
	err := decode(raw, &q)
	if log := os.Getenv("JARA_SYNC_RPC_LOG"); err == nil && log != "" {
		err = appendRPCLog(log, q.Op)
	}
	if err == nil {
		err = remoteRPC(q)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type commitSyncFixture struct {
	local, remote string
	remoteHost    Remote
	binding       Binding
}

func newCommitSyncFixture(t *testing.T) commitSyncFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("remote workspace locking requires Linux")
	}
	base := t.TempDir()
	t.Setenv("JARA_DATA", filepath.Join(base, "data"))
	t.Setenv("JARA_CONFIG", filepath.Join(base, "config", "config.json"))
	t.Setenv("TMUX_TMPDIR", filepath.Join(base, "tmux"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, "gitconfig"))
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "Sync Test")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "sync@example.invalid")
	}
	if err := os.MkdirAll(filepath.Join(base, "tmux"), 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(base, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor last; do :; done\neval \"set -- $last\"\nJARA_SYNC_RPC_TOKEN=\"$3\" exec " + quote(binary) + " -test.run='^$'\n"
	if err = os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	local := filepath.Join(base, "local")
	gitSyncTestCommand(t, "", "init", "-q", "-b", "main", local)
	writeSyncFile(t, local, "a.txt", "a base\n")
	writeSyncFile(t, local, "b.txt", "b base\n")
	commitSyncFixture{}.commit(t, local, "base")

	if err = remoteRPC(Request{Op: "create", ID: "w-sync", Name: "local"}); err != nil {
		t.Fatal(err)
	}
	w, err := workspaceInfo("w-sync")
	if err != nil {
		t.Fatal(err)
	}
	gitSyncTestCommand(t, "", "clone", "-q", local, w.Path)
	f := commitSyncFixture{local: local, remote: w.Path, remoteHost: Remote{Host: "fake"}, binding: Binding{Remote: "fake", Workspace: "w-sync"}}
	if err = seedCloneBaseline(f.remoteHost, f.binding, local); err != nil {
		t.Fatal(err)
	}
	return f
}

func (commitSyncFixture) commit(t *testing.T, dir, message string) {
	t.Helper()
	gitSyncTestCommand(t, dir, "add", "-A")
	gitSyncTestCommand(t, dir, "commit", "-q", "-m", message)
}

func (f commitSyncFixture) sync(direction string) error {
	return syncWorkspace(f.remoteHost, f.binding, f.local, direction, false, false, false)
}

func writeSyncFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

func readSyncFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func syncStatus(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(string(gitSyncTestCommand(t, dir, "status", "--porcelain")))
}

func TestSyncPushTransfersCommitsAndOnlyUncommittedEditsAsFiles(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.local, "a.txt", "a committed\n")
	writeSyncFile(t, f.local, "c.txt", "c added\n")
	f.commit(t, f.local, "local work")
	writeSyncFile(t, f.local, "b.txt", "b uncommitted\n")

	if err := f.sync("push"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.remote) != gitHead(f.local) {
		t.Fatal("remote checkout was not fast-forwarded to the local commit")
	}
	if status := syncStatus(t, f.remote); status != "M b.txt" {
		t.Fatalf("remote should show only the uncommitted edit, got %q", status)
	}
	if readSyncFile(t, f.remote, "b.txt") != "b uncommitted\n" || readSyncFile(t, f.remote, "c.txt") != "c added\n" {
		t.Fatal("remote files do not match local")
	}
	// A clean second push has nothing left to transfer.
	if err := f.sync("push"); err != nil {
		t.Fatal(err)
	}
}

func TestSyncPushAdoptsFilesAlreadyCopiedAsEdits(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.local, "a.txt", "a committed\n")
	writeSyncFile(t, f.local, "c.txt", "c added\n")
	f.commit(t, f.local, "local work")
	// What the earlier file-only sync left behind on the remote.
	writeSyncFile(t, f.remote, "a.txt", "a committed\n")
	writeSyncFile(t, f.remote, "c.txt", "c added\n")

	if err := f.sync("push"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.remote) != gitHead(f.local) || syncStatus(t, f.remote) != "" {
		t.Fatalf("remote not converged to the commit: status %q", syncStatus(t, f.remote))
	}
}

// An earlier sync mirrored edits that were committed and then changed again:
// the remote copy matches an intermediate commit, so the push keeps it.
func TestSyncPushAdoptsEditsFromAnIntermediateCommit(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.local, "a.txt", "a first version\n")
	f.commit(t, f.local, "first")
	writeSyncFile(t, f.remote, "a.txt", "a first version\n")
	writeSyncFile(t, f.local, "a.txt", "a second version\n")
	f.commit(t, f.local, "second")

	if err := f.sync("push"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.remote) != gitHead(f.local) || readSyncFile(t, f.remote, "a.txt") != "a second version\n" || syncStatus(t, f.remote) != "" {
		t.Fatalf("remote not converged: status %q", syncStatus(t, f.remote))
	}
}

func TestSyncPushRefusesWithoutChangingRemote(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.local, "a.txt", "a committed\n")
	f.commit(t, f.local, "local work")
	before := gitHead(f.remote)

	writeSyncFile(t, f.remote, "a.txt", "a remote edit\n")
	err := f.sync("push")
	if err == nil || !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "overwritten") {
		t.Fatalf("overlapping remote edit was not reported: %v", err)
	}
	if gitHead(f.remote) != before || readSyncFile(t, f.remote, "a.txt") != "a remote edit\n" {
		t.Fatal("refused push changed the remote")
	}

	gitSyncTestCommand(t, f.remote, "checkout", "-q", "--", "a.txt")
	writeSyncFile(t, f.remote, "d.txt", "remote commit\n")
	f.commit(t, f.remote, "remote work")
	if err = f.sync("push"); err == nil || !strings.Contains(err.Error(), "jara sync pull") {
		t.Fatalf("remote-only commit was not reported: %v", err)
	}

	gitSyncTestCommand(t, f.remote, "checkout", "-q", "-b", "agent")
	if err = f.sync("push"); err == nil || !strings.Contains(err.Error(), `"agent"`) {
		t.Fatalf("branch mismatch was not reported: %v", err)
	}
}

func TestSyncPullFastForwardsLocalCheckout(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.remote, "a.txt", "a agent\n")
	writeSyncFile(t, f.remote, "e.txt", "e agent\n")
	f.commit(t, f.remote, "agent work")
	writeSyncFile(t, f.remote, "b.txt", "b agent draft\n")
	writeSyncFile(t, f.local, "notes.txt", "local only\n")

	if err := f.sync("pull"); err != nil {
		t.Fatal(err)
	}
	if gitHead(f.local) != gitHead(f.remote) {
		t.Fatal("local checkout was not fast-forwarded to the remote commit")
	}
	if status := syncStatus(t, f.local); status != "M b.txt\n?? notes.txt" {
		t.Fatalf("local should keep only uncommitted changes, got %q", status)
	}
	if readSyncFile(t, f.local, "e.txt") != "e agent\n" {
		t.Fatal("pulled commit content missing")
	}

	// Local commits the remote lacks block a pull without changing anything.
	writeSyncFile(t, f.local, "notes.txt", "local only, committed\n")
	f.commit(t, f.local, "local notes")
	before := gitHead(f.local)
	if err := f.sync("pull"); err == nil || !strings.Contains(err.Error(), "jara sync push") {
		t.Fatalf("local-only commit was not reported: %v", err)
	}
	if gitHead(f.local) != before {
		t.Fatal("refused pull moved the local checkout")
	}
}
