package jara

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// appendRPCLog records an operation the fake remote served.
func appendRPCLog(path, op string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(op + "\n")
	return err
}

// logRPCs makes the fake remote record each operation; the returned function
// lists them and starts a new record.
func logRPCs(t *testing.T) func() []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rpc.log")
	t.Setenv("JARA_SYNC_RPC_LOG", path)
	return func() []string {
		data, _ := os.ReadFile(path)
		_ = os.Remove(path)
		return strings.Fields(string(data))
	}
}

func (f commitSyncFixture) claimed(t *testing.T) *remoteWorkspaceState {
	t.Helper()
	head := gitHead(f.local)
	state, err := readRemoteWorkspaceState(Request{ID: f.binding.Workspace, Commit: head})
	if err != nil {
		t.Fatal(err)
	}
	return claimedState(state, head)
}

func TestClaimedStateSkipsSyncLookups(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.local, "b.txt", "b uncommitted\n")
	known := f.claimed(t)
	rpcs := logRPCs(t)

	if err := syncWorkspaceKnown(f.remoteHost, f.binding, f.local, "push", false, known, false, false); err != nil {
		t.Fatal(err)
	}
	if got := rpcs(); !reflect.DeepEqual(got, []string{"apply"}) {
		t.Fatalf("remote calls = %v, want only apply", got)
	}
	if readSyncFile(t, f.remote, "b.txt") != "b uncommitted\n" {
		t.Fatal("edit was not applied")
	}
	if known.Git != nil || known.Files != nil {
		t.Fatal("a sync must use up what it was told")
	}
}

func TestClaimedFilesAreListedAgainAfterCommitsMove(t *testing.T) {
	f := newCommitSyncFixture(t)
	known := f.claimed(t)
	writeSyncFile(t, f.local, "a.txt", "a committed\n")
	f.commit(t, f.local, "local work")
	writeSyncFile(t, f.local, "b.txt", "b uncommitted\n")
	// The claim answered for the old commit, so its Git state is asked again.
	rpcs := logRPCs(t)

	if err := syncWorkspaceKnown(f.remoteHost, f.binding, f.local, "push", false, known, false, false); err != nil {
		t.Fatal(err)
	}
	if got := rpcs(); !reflect.DeepEqual(got, []string{"git-state", "git-advance", "manifest", "apply"}) {
		t.Fatalf("remote calls = %v", got)
	}
	if gitHead(f.remote) != gitHead(f.local) || readSyncFile(t, f.remote, "b.txt") != "b uncommitted\n" {
		t.Fatal("remote checkout does not match the local one")
	}
	if status := syncStatus(t, f.remote); status != "M b.txt" {
		t.Fatalf("remote status = %q", status)
	}
}

func TestBranchAlignmentSharesTheGitStateWithTheSync(t *testing.T) {
	f := newCommitSyncFixture(t)
	writeSyncFile(t, f.local, "b.txt", "b uncommitted\n")
	o := localRunnerOpen{remote: f.remoteHost, remoteName: "fake", binding: f.binding, root: f.local, isGit: true, known: &remoteWorkspaceState{}}
	rpcs := logRPCs(t)

	if err := pushBeforeRunning(o, "main"); err != nil {
		t.Fatal(err)
	}
	if got := rpcs(); !reflect.DeepEqual(got, []string{"git-state", "manifest", "apply"}) {
		t.Fatalf("remote calls = %v, want one git-state", got)
	}
}

func TestBranchSwitchForgetsTheClaimedState(t *testing.T) {
	f := newCommitSyncFixture(t)
	known := f.claimed(t)
	gitSyncTestCommand(t, f.local, "switch", "-q", "-c", "feature")
	o := localRunnerOpen{remote: f.remoteHost, remoteName: "fake", binding: f.binding, root: f.local, isGit: true, known: known}
	rpcs := logRPCs(t)

	if err := pushBeforeRunning(o, "main"); err != nil {
		t.Fatal(err)
	}
	got := rpcs()
	if len(got) < 2 || got[0] != "git-switch" || got[1] != "git-state" {
		t.Fatalf("remote calls = %v, want a fresh git-state after the switch", got)
	}
	if branch := checkoutState(f.remote).Branch; branch != "feature" {
		t.Fatalf("remote branch = %q", branch)
	}
}

func TestRunnerClaimReturnsSyncStateOnRequest(t *testing.T) {
	f := newCommitSyncFixture(t)
	head := gitHead(f.local)
	var claim runnerClaim
	q := Request{Op: "runner-claim", ID: f.binding.Workspace, Runner: "laptop", Wait: true, SyncState: true, Commit: head}
	if err := rpc(f.remoteHost, q, nil, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Wait || claim.State == nil || claim.State.Git == nil {
		t.Fatalf("claim = %+v", claim)
	}
	if claim.State.Git.Head != head || claim.State.Git.Contains == nil || !*claim.State.Git.Contains {
		t.Fatalf("git state = %+v", claim.State.Git)
	}
	if _, ok := claim.State.Files["a.txt"]; !ok {
		t.Fatalf("files = %v", claim.State.Files)
	}

	var plain runnerClaim
	q.SyncState = false
	if err := rpc(f.remoteHost, q, nil, &plain); err != nil {
		t.Fatal(err)
	}
	if plain.State != nil {
		t.Fatal("a claim that did not ask received the sync state")
	}
}

func TestShortCallsShareOneConnectionPerRemoteIdentity(t *testing.T) {
	t.Setenv("JARA_SSH_MULTIPLEX", "")
	t.Setenv("JARA_SSH_CONFIG", "")
	// t.TempDir is too long a path for sockets.
	runtimeDir, err := os.MkdirTemp("/tmp", "jara-run-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	controlPath := func(r Remote) string {
		args := sshMuxArgs(r)
		for _, arg := range args {
			if path, ok := strings.CutPrefix(arg, "ControlPath="); ok {
				return path
			}
		}
		t.Fatalf("no ControlPath in %v", args)
		return ""
	}
	work := controlPath(Remote{Host: "work", Port: 822})
	if filepath.Dir(work) != filepath.Join(runtimeDir, "jara-ssh") {
		t.Fatalf("control path %s is outside the runtime directory", work)
	}
	if work != controlPath(Remote{Host: "work", Port: 822}) {
		t.Fatal("the same remote must reuse its connection")
	}
	if work == controlPath(Remote{Host: "work", Port: 822, Identity: "/keys/other"}) {
		t.Fatal("a remote with another key must not share a connection")
	}
	if args := strings.Join(sshMuxArgs(Remote{Host: "work"}), " "); !strings.Contains(args, "ControlMaster=auto") || !strings.Contains(args, "ControlPersist="+sshMuxPersist) {
		t.Fatal(args)
	}

	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), strings.Repeat("long", 20)))
	if path := controlPath(Remote{Host: "work"}); filepath.Dir(path) != filepath.Join("/tmp", fmt.Sprintf("jara-ssh-%d", os.Getuid())) {
		t.Fatalf("a runtime directory too long for sockets was not skipped: %s", path)
	}

	t.Setenv("JARA_SSH_MULTIPLEX", "0")
	if args := sshMuxArgs(Remote{Host: "work"}); args != nil {
		t.Fatalf("JARA_SSH_MULTIPLEX=0 still shares: %v", args)
	}
}

func TestConnectionSharingNeedsAPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "jara-ssh")
	if !privateDir(dir) {
		t.Fatal("a new directory is private")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if privateDir(dir) {
		t.Fatal("a directory others can enter is not private")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if privateDir(link) {
		t.Fatal("a link is not a private directory")
	}
}
