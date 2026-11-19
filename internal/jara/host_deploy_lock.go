package jara

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A guided deployment is locked on its Docker host while a client changes it.
// The lock directory holds an owner record naming the client process and the
// phase it reached, so a later run can clear the lock of a client that died
// (for example after Ctrl+C) while still refusing to touch a deployment that
// stopped mid-replacement and may need manual recovery.

const (
	deployLockPrepare   = "prepare"
	deployLockReplacing = "replacing"
	deployLockCleanup   = "cleanup"
	deployLockRecovery  = "recovery"

	deployLockHeader = "jara-deploy-lock 1"
	// deployLockExists is the remote exit status for "lock already taken".
	deployLockExists = 75
)

var deployLockStageSuffix = regexp.MustCompile(`^[a-f0-9]{16}$`)

type deployLockOwner struct {
	Purpose string
	Client  string
	PID     int
	Started string
	Phase   string
	Stage   string
}

func (o deployLockOwner) encode() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\npurpose=%s\nclient=%s\npid=%d\nstarted=%s\nphase=%s\n", deployLockHeader, o.Purpose, o.Client, o.PID, o.Started, o.Phase)
	if o.Stage != "" {
		fmt.Fprintf(&b, "stage=%s\n", o.Stage)
	}
	return b.Bytes()
}

func parseDeployLockOwner(data []byte) (deployLockOwner, bool) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 0 || lines[0] != deployLockHeader {
		return deployLockOwner{}, false
	}
	var o deployLockOwner
	for _, line := range lines[1:] {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "purpose":
			o.Purpose = value
		case "client":
			o.Client = value
		case "pid":
			o.PID, _ = strconv.Atoi(value)
		case "started":
			o.Started = value
		case "phase":
			o.Phase = value
		case "stage":
			o.Stage = value
		}
	}
	return o, o.Client != "" && o.PID > 0 && o.Phase != ""
}

// blocker explains why the holder's lock must stay, or returns "" when the
// holder is a dead process on this computer that left the deployment intact.
func (o deployLockOwner) blocker(client string) string {
	switch {
	case o.Phase == deployLockRecovery:
		return fmt.Sprintf("an earlier %s could not restore the deployment and kept its lock for recovery; check the deployment, then remove the lock", o.Purpose)
	case o.Phase == deployLockReplacing:
		return fmt.Sprintf("an earlier %s (started %s) stopped while replacing the container; check the deployment, then remove the lock", o.Purpose, o.Started)
	case o.Phase != deployLockPrepare && o.Phase != deployLockCleanup:
		return fmt.Sprintf("the deployment lock records unknown phase %q; inspect it before removing it", o.Phase)
	case client == "" || o.Client != client:
		return fmt.Sprintf("a %s from %s (process %d, started %s) holds the deployment lock; if it is no longer running, remove the lock", o.Purpose, o.Client, o.PID, o.Started)
	case processAlive(o.PID):
		return fmt.Sprintf("a %s is already running on this computer (process %d, started %s)", o.Purpose, o.PID, o.Started)
	}
	return ""
}

type remoteDeployLock struct {
	runner hostSSHRunner
	path   string
	owner  deployLockOwner
}

// acquireRemoteDeployLock takes the deployment lock at lockPath. stagePrefix
// names where this kind of operation stages files, so clearing a stale lock
// can also remove the dead run's staging directory; it may be empty.
func acquireRemoteDeployLock(runner hostSSHRunner, lockPath, stagePrefix, purpose string, log io.Writer) (*remoteDeployLock, error) {
	client, _ := os.Hostname()
	l := &remoteDeployLock{runner: runner, path: lockPath, owner: deployLockOwner{
		Purpose: purpose,
		Client:  client,
		PID:     os.Getpid(),
		Started: time.Now().Format(time.RFC3339),
		Phase:   deployLockPrepare,
	}}
	if l.owner.Client == "" {
		l.owner.Client = "unknown"
	}
	ownerPath := path.Join(lockPath, "owner")
	create := "if ! mkdir -m 700 -- " + quote(lockPath) + " 2>/dev/null; then [ -e " + quote(lockPath) + " ] && exit " + strconv.Itoa(deployLockExists) + "; mkdir -m 700 -- " + quote(lockPath) + "; exit 1; fi; " +
		"if ! cat > " + quote(ownerPath) + "; then rm -f -- " + quote(ownerPath) + "; rmdir -- " + quote(lockPath) + "; exit 1; fi"
	for cleared := false; ; cleared = true {
		_, err := runner.shell(20*time.Second, create, bytes.NewReader(l.owner.encode()), false)
		if err == nil {
			return l, nil
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != deployLockExists {
			return nil, fmt.Errorf("take deployment lock %s: %w", lockPath, err)
		}
		if cleared {
			return nil, fmt.Errorf("another deployment operation took the lock %s", lockPath)
		}
		record, readErr := runner.shell(10*time.Second, "cat -- "+quote(ownerPath), nil, false)
		holder, known := parseDeployLockOwner(record)
		if readErr != nil || !known {
			return nil, fmt.Errorf("another deployment operation may be running; if not, inspect and remove %s before retrying", lockPath)
		}
		if reason := holder.blocker(client); reason != "" {
			return nil, fmt.Errorf("%s: %s", reason, lockPath)
		}
		if err = l.clearStale(record, holder.Stage, stagePrefix); err != nil {
			return nil, fmt.Errorf("clear stale deployment lock %s: %w", lockPath, err)
		}
		fmt.Fprintf(log, "Cleared a stale deployment lock left by an interrupted %s (process %d, started %s).\n", holder.Purpose, holder.PID, holder.Started)
	}
}

// clearStale removes the lock only if its owner record is still the one that
// was judged stale, together with the dead run's staging directory.
func (l *remoteDeployLock) clearStale(record []byte, stage, stagePrefix string) error {
	ownerPath := path.Join(l.path, "owner")
	command := "cmp -s - " + quote(ownerPath) + " || exit 1; "
	if stagePrefix != "" && path.Clean(stage) == stage && strings.HasPrefix(stage, stagePrefix) && deployLockStageSuffix.MatchString(strings.TrimPrefix(stage, stagePrefix)) {
		command += "rm -rf -- " + quote(stage) + " && "
	}
	command += "rm -f -- " + quote(ownerPath) + " " + quote(ownerPath+".tmp") + " && rmdir -- " + quote(l.path)
	_, err := l.runner.shell(2*time.Minute, command, bytes.NewReader(record), false)
	return err
}

// record updates the owner record with the phase reached and the staging
// directory in use.
func (l *remoteDeployLock) record(phase, stage string) error {
	owner := l.owner
	owner.Phase, owner.Stage = phase, stage
	ownerPath := path.Join(l.path, "owner")
	command := "cat > " + quote(ownerPath+".tmp") + " && mv -f -- " + quote(ownerPath+".tmp") + " " + quote(ownerPath)
	if _, err := l.runner.shell(10*time.Second, command, bytes.NewReader(owner.encode()), false); err != nil {
		return fmt.Errorf("record deployment lock phase: %w", err)
	}
	l.owner = owner
	return nil
}

func (l *remoteDeployLock) release() error {
	ownerPath := path.Join(l.path, "owner")
	_, err := l.runner.shell(20*time.Second, "rm -f -- "+quote(ownerPath)+" "+quote(ownerPath+".tmp")+" && rmdir -- "+quote(l.path), nil, false)
	return err
}
