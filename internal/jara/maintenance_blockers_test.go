//go:build linux

package jara

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHConnectionBlockers(t *testing.T) {
	processes := map[int]processInfo{
		1:  {parent: 0, command: "init"},
		10: {parent: 1, sshd: true, command: "sshd: /usr/sbin/sshd -D [listener]"},
		20: {parent: 10, sshd: true, command: "sshd-session: alice [priv]"},
		21: {parent: 20, sshd: true, command: "sshd-session: alice@pts/0"},
		22: {parent: 21, command: "bash"},
		23: {parent: 22, command: "vim"},
		30: {parent: 10, sshd: true, command: "sshd-session: bob [priv]"},
		31: {parent: 30, sshd: true, command: "sshd-session: bob@notty"},
		32: {parent: 31, command: "jara _chat-server"},
		40: {parent: 10, sshd: true, command: "sshd-session: unknown [net]"},
	}
	roots := sshConnectionRoots(processes, map[int]bool{1: true, 10: true, 30: true, 31: true})
	if len(roots) != 2 || roots[0] != 20 || roots[1] != 40 {
		t.Fatalf("connection roots = %v, want alice's and the connecting one", roots)
	}
	if got := describeSSHConnection(20, processes); got != "SSH connection by alice running bash" {
		t.Fatalf("alice = %q", got)
	}
	if got := describeSSHConnection(40, processes); got != "SSH connection (no program running yet)" {
		t.Fatalf("connecting = %q", got)
	}
	for title, want := range map[string]string{
		"sshd-session: alice [priv]": "alice", "sshd: bob@pts/1": "bob", "sshd-session": "", "": "",
	} {
		if got := sshConnectionUser(title); got != want {
			t.Errorf("user of %q = %q, want %q", title, got, want)
		}
	}
}

// runMaintenance runs `jara _maintenance` and decodes its status.
func runMaintenance(t *testing.T, args ...string) maintenanceStatus {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = write
	runErr := maintenanceCommand(args)
	os.Stdout = stdout
	_ = write.Close()
	data, _ := io.ReadAll(read)
	if runErr != nil {
		t.Fatalf("maintenance %v: %v", args, runErr)
	}
	var status maintenanceStatus
	if err = json.Unmarshal(data, &status); err != nil {
		t.Fatalf("maintenance %v output %q: %v", args, data, err)
	}
	return status
}

func TestForcedMaintenanceTakesLeaseAndListsBlockers(t *testing.T) {
	useFakeDaemonTools(t, "jara-demo||0|1\n")
	t.Setenv("JARA_DATA", t.TempDir())
	createChatDaemonWorkspace(t, "w-op", true)
	lock := filepath.Join(os.Getenv("JARA_DATA"), "workspaces", "w-op.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lock, workspaceLockOwnerFile), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	status := runMaintenance(t, "status")
	joined := strings.Join(status.Blockers, "\n")
	for _, want := range []string{"terminal session jara-demo (attached)", "workspace operation in demo (w-op)"} {
		if !status.Busy || !strings.Contains(joined, want) {
			t.Fatalf("status blockers lack %q: %+v", want, status)
		}
	}
	if deferred := runMaintenance(t, "begin", "update-1"); !deferred.Busy || deferred.Forced {
		t.Fatalf("unforced begin = %+v, want deferred", deferred)
	}
	if _, active, _ := activeMaintenance(); active {
		t.Fatal("unforced begin took the lease")
	}
	forced := runMaintenance(t, "begin", "update-1", "force")
	if !forced.Forced || !strings.Contains(strings.Join(forced.Blockers, "\n"), "jara-demo") {
		t.Fatalf("forced begin = %+v", forced)
	}
	lease, active, err := activeMaintenance()
	if err != nil || !active || lease.Token != "update-1" || time.Until(lease.Expires) < 9*time.Minute {
		t.Fatalf("forced begin lease = %+v active=%v err=%v", lease, active, err)
	}
	if again := runMaintenance(t, "status"); !strings.Contains(strings.Join(again.Blockers, "\n"), "another update holds the maintenance lease") {
		t.Fatalf("status during the update = %+v", again)
	}
}

func TestLeaseRemainingIsZoneFree(t *testing.T) {
	if got := leaseRemaining(time.Now().Add(9*time.Minute + 30*time.Second + 200*time.Millisecond)); got != "another 9m30s" {
		t.Fatalf("leaseRemaining = %q", got)
	}
	if got := leaseRemaining(time.Now().Add(-time.Minute)); got != "under a second more" {
		t.Fatalf("expired leaseRemaining = %q", got)
	}
}
