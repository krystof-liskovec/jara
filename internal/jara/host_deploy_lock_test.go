package jara

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// localDeployLockRunner returns a runner whose stand-in ssh runs each remote
// command on this machine.
func localDeployLockRunner(t *testing.T) hostSSHRunner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stand-in ssh needs a POSIX shell")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nfor a; do last=$a; done\nexec sh -c \"$last\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return hostSSHRunner{remote: Remote{Host: "docker-host"}}
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func writeLockOwner(t *testing.T, lock string, owner []byte) {
	t.Helper()
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lock, "owner"), owner, 0600); err != nil {
		t.Fatal(err)
	}
}

func thisClient(t *testing.T) string {
	t.Helper()
	client, err := os.Hostname()
	if err != nil || client == "" {
		t.Skip("hostname unavailable")
	}
	return client
}

func TestDeployLockAcquireRecordRelease(t *testing.T) {
	runner := localDeployLockRunner(t)
	lock := filepath.Join(t.TempDir(), ".jara-deploy-x.upgrade.lock")
	l, err := acquireRemoteDeployLock(runner, lock, "", "Jara upgrade to 1.2.3", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if err = l.record(deployLockReplacing, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(lock, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := parseDeployLockOwner(data)
	if !ok || owner.PID != os.Getpid() || owner.Phase != deployLockReplacing || owner.Purpose != "Jara upgrade to 1.2.3" {
		t.Fatalf("owner record = %q", data)
	}
	if _, err = acquireRemoteDeployLock(runner, lock, "", "SSH key change", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "stopped while replacing") {
		t.Fatalf("second acquire error = %v", err)
	}
	if err = l.release(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("lock still present: %v", err)
	}
}

func TestDeployLockClearsDeadClientLockAndStage(t *testing.T) {
	runner := localDeployLockRunner(t)
	home := t.TempDir()
	lock := filepath.Join(home, ".jara-deploy-x.upgrade.lock")
	prefix := filepath.Join(home, ".jara-upgrade-x-")
	stage := prefix + "0123456789abcdef"
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	writeLockOwner(t, lock, deployLockOwner{Purpose: "Jara upgrade to 1.0.0", Client: thisClient(t), PID: deadPID(t), Started: "earlier", Phase: deployLockPrepare, Stage: stage}.encode())
	var log bytes.Buffer
	l, err := acquireRemoteDeployLock(runner, lock, prefix, "Jara upgrade to 1.0.1", &log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "Cleared a stale deployment lock left by an interrupted Jara upgrade to 1.0.0") {
		t.Fatalf("log = %q", log.String())
	}
	if _, err = os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("stale stage still present: %v", err)
	}
	if err = l.release(); err != nil {
		t.Fatal(err)
	}
}

func TestDeployLockKeepsLocksItCannotProveStale(t *testing.T) {
	runner := localDeployLockRunner(t)
	client := thisClient(t)
	dead := deadPID(t)
	cases := map[string]struct {
		owner []byte
		want  string
	}{
		"legacy record": {[]byte("0.1.0-dev\n"), "inspect and remove"},
		"live process":  {deployLockOwner{Purpose: "SSH key change", Client: client, PID: os.Getpid(), Started: "now", Phase: deployLockPrepare}.encode(), "already running on this computer"},
		"other client":  {deployLockOwner{Purpose: "SSH key change", Client: client + "-other", PID: dead, Started: "now", Phase: deployLockPrepare}.encode(), "from " + client + "-other"},
		"mid replace":   {deployLockOwner{Purpose: "Jara upgrade to 1.0.0", Client: client, PID: dead, Started: "now", Phase: deployLockReplacing}.encode(), "stopped while replacing"},
		"recovery":      {deployLockOwner{Purpose: "Jara upgrade to 1.0.0", Client: client, PID: dead, Started: "now", Phase: deployLockRecovery}.encode(), "kept its lock for recovery"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), ".jara-deploy-x.upgrade.lock")
			writeLockOwner(t, lock, tc.owner)
			_, err := acquireRemoteDeployLock(runner, lock, "", "Jara upgrade to 2.0.0", &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), lock) {
				t.Fatalf("error = %v, want it to mention %q and the lock path", err, tc.want)
			}
			if data, readErr := os.ReadFile(filepath.Join(lock, "owner")); readErr != nil || !bytes.Equal(data, tc.owner) {
				t.Fatalf("owner record changed: %q, %v", data, readErr)
			}
		})
	}
}
