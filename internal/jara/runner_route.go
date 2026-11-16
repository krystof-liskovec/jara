package jara

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"krystof.liskovec/jara/internal/agentrouter"
)

var errRunnerUnchanged = errors.New("runner unchanged")

// hubRunnerRoute prepares a session request on the remote host. It returns a
// connection to the online device running the workspace, or nil when the
// host runs the agent itself: after replacing an offline device and
// installing conversations devices checkpointed since the host last ran.
func hubRunnerRoute(q *Request) (net.Conn, error) {
	paths, err := runnerPathSet(q.ID)
	if err != nil {
		return nil, err
	}
	lease, exists, err := loadRunnerLease(paths, q.ID)
	if err != nil || !exists {
		return nil, err
	}
	if lease.Owner != "" && lease.online() {
		if conn, dialErr := dialRunner(paths, lease); dialErr == nil {
			return conn, nil
		}
		return nil, fmt.Errorf("%s runs this conversation and is reconnecting; try again shortly", lease.Owner)
	}
	imported := false
	lease, err = updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
		if lease.Owner != "" {
			if lease.online() {
				return fmt.Errorf("%s came back online; try again", lease.Owner)
			}
			lease.replaceDevice()
		}
		before := lease.HubSeq
		if importErr := importHubCheckpoint(q.ID, paths, lease); importErr != nil {
			fmt.Fprintln(os.Stderr, "Could not restore the conversation checkpoint:", importErr)
		}
		imported = lease.HubSeq != before
		lease.LastRunner = runnerHub
		return nil
	})
	if err != nil {
		return nil, err
	}
	if imported && q.Conversation == "" && !q.New {
		q.Conversation, q.Resume, q.ResumeLatest = lease.Conversation, false, false
	}
	return nil, nil
}

// continueOnHub runs when a device goes to sleep or offline while it holds
// the workspace. A device that was idle keeps it until someone connects
// elsewhere; one with a running turn or scheduled work is replaced at once
// and the host continues that work from the last checkpoint.
func continueOnHub(id, reason string, applies func(runnerLease) bool) error {
	paths, err := runnerPathSet(id)
	if err != nil {
		return err
	}
	var device, conversation string
	var busy bool
	_, err = updateRunnerLease(paths, id, func(lease *runnerLease) error {
		if lease.Owner == "" || !applies(*lease) || !(lease.Busy || lease.Pending) {
			return errRunnerUnchanged
		}
		device, conversation, busy = lease.Owner, lease.Conversation, lease.Busy
		lease.replaceDevice()
		if importErr := importHubCheckpoint(id, paths, lease); importErr != nil {
			fmt.Fprintln(os.Stderr, "Could not restore the conversation checkpoint:", importErr)
		}
		lease.LastRunner = runnerHub
		return nil
	})
	if errors.Is(err, errRunnerUnchanged) {
		return nil
	}
	if err != nil || conversation == "" {
		return err
	}
	q := Request{UserInterface: "jara", ID: id, Conversation: conversation, Headless: true}
	if busy {
		q.Continue = fmt.Sprintf("[Jara: %s %s while you were working on the latest request. "+
			"The working tree holds the changes up to its last checkpoint. Check where the work stands, then continue the unfinished task.]", device, reason)
	}
	conn, _, err := ensureChatDaemon(q)
	if err != nil {
		return fmt.Errorf("continue on the remote host: %w", err)
	}
	return conn.Close()
}

// hubRunnerDaemon prepares the host's session of a workspace that devices
// also run: it keeps a transcript of every conversation so devices can
// continue them, hands the workspace back to a waiting device when idle, and
// starts work continued from a device without a window.
func hubRunnerDaemon(q Request, server *exec.Cmd, schedules *chatScheduleManager) (chatDaemonRunnerHooks, error) {
	if !runnerLeaseExists(q.ID) {
		return chatDaemonRunnerHooks{}, nil
	}
	paths, err := runnerPathSet(q.ID)
	if err != nil {
		return chatDaemonRunnerHooks{}, err
	}
	server.Env = append(os.Environ(), agentrouter.PortableThreadsEnv+"=1")
	hooks := chatDaemonRunnerHooks{handBack: func() bool {
		if lease, _, err := loadRunnerLease(paths, q.ID); err != nil || len(lease.Wants) == 0 {
			return false
		}
		// The first hand-back of a round starts the preferred device's grace.
		lease, err := updateRunnerLease(paths, q.ID, func(lease *runnerLease) error {
			if lease.HandBackAt == 0 {
				lease.HandBackAt = time.Now().Unix()
			}
			return nil
		})
		return err == nil && len(lease.Wants) > 0
	}}
	if q.Headless && q.Conversation != "" {
		hooks.bootstrap = q.Conversation
		if q.Continue != "" {
			if _, err = schedules.add(q.Conversation, q.Continue, time.Now().Add(time.Second)); err != nil {
				return hooks, err
			}
		}
	}
	return hooks, nil
}
