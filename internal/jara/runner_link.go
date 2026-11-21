package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// runnerHeartbeat is what a device reports over its link every few seconds.
type runnerHeartbeat struct {
	Busy         bool   `json:"busy,omitempty"`
	Pending      bool   `json:"pending,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Sleep        bool   `json:"sleep,omitempty"`    // the device is about to suspend or stop
	Shutdown     bool   `json:"shutdown,omitempty"` // with Sleep: it shuts down or its session was stopped
}

// runRunnerLink is `jara _runner-link` on the remote host: the command of
// the SSH connection that also carries a device's tunnel. It renews the lease
// with every heartbeat on standard input. A device about to sleep says so,
// and the host continues its running work at once; a device that just goes
// silent is noticed by runRunnerWatch.
func runRunnerLink(q Request) error {
	// sshd may hang up on this process when the connection drops.
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	paths, err := runnerPathSet(q.ID)
	if err != nil {
		return err
	}
	if q.Link == "" {
		return fmt.Errorf("invalid runner link")
	}
	_, err = updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
		if !lease.holds(q.Runner, q.Epoch) {
			return errRunnerLost
		}
		lease.Link, lease.LastSeen = q.Link, time.Now().Unix()
		return nil
	})
	if err != nil {
		return err
	}
	startRunnerWatch(q.ID, paths)
	scanner := protocol.NewLineScanner(os.Stdin)
	for scanner.Scan() {
		var beat runnerHeartbeat
		if json.Unmarshal(scanner.Bytes(), &beat) != nil {
			continue
		}
		_, err = updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
			if !lease.holds(q.Runner, q.Epoch) || lease.Link != q.Link {
				return errRunnerLost
			}
			lease.LastSeen, lease.Busy, lease.Pending = time.Now().Unix(), beat.Busy, beat.Pending
			if beat.Conversation != "" {
				lease.Conversation = beat.Conversation
			}
			return nil
		})
		if err != nil {
			return err // replaced; the device's next tunnel attempt learns it
		}
		if beat.Sleep {
			reason := "went to sleep"
			if beat.Shutdown {
				reason = "shut down"
			}
			return continueOnHub(q.ID, reason, func(lease runnerLease) bool { return lease.Link == q.Link })
		}
	}
	return nil // a silent or disconnected device is runRunnerWatch's to handle
}

// runRunnerWatch is `jara _runner-watch`, a detached process on the remote
// host that lives while devices hold the workspace. When a device with a
// running turn or scheduled work stops renewing its lease, the host continues
// that work.
func runRunnerWatch(q Request) error {
	paths, err := runnerPathSet(q.ID)
	if err != nil {
		return err
	}
	pidPath := paths.lease + ".watch"
	file, err := os.OpenFile(pidPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil // another watcher runs
	}
	fmt.Fprintf(file, "%d\n", os.Getpid())
	file.Close()
	defer os.Remove(pidPath)
	for {
		time.Sleep(time.Second)
		lease, _, err := loadRunnerLease(paths, q.ID)
		if err != nil || lease.Owner == "" {
			return err
		}
		// An idle device that went offline keeps the workspace; keep watching
		// in case it reports work, or until another machine takes over.
		if !lease.online() && (lease.Busy || lease.Pending) {
			if err = continueOnHub(q.ID, "went offline", func(current runnerLease) bool { return !current.online() }); err != nil {
				return err
			}
		}
	}
}

func startRunnerWatch(id string, paths runnerPaths) {
	pidPath := paths.lease + ".watch"
	if data, err := os.ReadFile(pidPath); err == nil {
		var pid int
		if _, scanErr := fmt.Sscan(string(data), &pid); scanErr == nil && pid > 1 && chatProcessExists(pid) {
			return
		}
		_ = os.Remove(pidPath)
	}
	if _, err := startDetachedJara("_runner-watch", Request{ID: id}, paths.lease+".watch.log"); err != nil {
		fmt.Fprintln(os.Stderr, "jara: start runner watch:", err)
	}
}
