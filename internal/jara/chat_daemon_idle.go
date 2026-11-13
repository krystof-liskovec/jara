package jara

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// A persistent Jara session holds its workspace lock while it waits for a
// client to reconnect. Unlike a terminal agent, it reports whether a turn is
// running, so an idle, detached session need not block other operations: it
// can stop, and the next connection restarts it on the saved conversation.

// chatDaemonIdleState reports whether the workspace has a running persistent
// session and, if so, why it cannot stop now ("" when it is idle). With
// allowReconnecting, attached clients that reconnect by themselves do not count.
func chatDaemonIdleState(id string, allowReconnecting bool) (running bool, busy string) {
	paths, err := chatDaemonPathSet(id)
	if err != nil || chatDaemonPID(paths) == 0 {
		return false, ""
	}
	state, ok := readChatDaemonState(id)
	switch {
	case !ok:
		return true, "the persistent Jara session state is unreadable"
	case state.ActiveTurn:
		return true, "a Jara turn is running in this workspace"
	case state.Attached && state.AttachedClients == 0:
		return true, "a Jara client is attached to this workspace"
	case state.AttachedClients > 0 && !(allowReconnecting && state.ReconnectingClients >= state.AttachedClients):
		return true, "a Jara client is attached to this workspace"
	case chatWorkPending(paths):
		return true, "the persistent Jara session has scheduled or crunch work pending"
	}
	return true, ""
}

// idleChatDaemonWorkspace reports the workspace ID when the lock is held only
// by an idle persistent session (see chatDaemonIdleState).
func idleChatDaemonWorkspace(lock string, allowReconnecting bool) (string, bool) {
	id := strings.TrimSuffix(filepath.Base(lock), ".lock")
	if !nameRE.MatchString(id) {
		return "", false
	}
	daemon := lockMarkerPID(filepath.Join(lock, "chat-daemon.pid"))
	if daemon == 0 {
		return "", false
	}
	if owner := lockMarkerPID(filepath.Join(lock, workspaceLockOwnerFile)); owner != 0 && owner != daemon {
		return "", false
	}
	if state, ok := readChatDaemonState(id); !ok || state.PID != daemon {
		return "", false
	}
	if running, busy := chatDaemonIdleState(id, allowReconnecting); !running || busy != "" {
		return "", false
	}
	return id, true
}

func lockMarkerPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 2 {
		return 0
	}
	return pid
}

// chatWorkPending reads the saved schedule and crunch stores directly; loading
// them through their managers would rewrite interrupted jobs.
func chatWorkPending(paths chatDaemonPaths) bool {
	var schedules []struct {
		Status string `json:"status"`
	}
	if data, err := os.ReadFile(paths.schedules); err == nil {
		if json.Unmarshal(data, &schedules) != nil {
			return true
		}
		for _, job := range schedules {
			if job.Status == "pending" || job.Status == "running" {
				return true
			}
		}
	} else if !os.IsNotExist(err) {
		return true
	}
	var crunch struct {
		Status string `json:"status"`
	}
	if data, err := os.ReadFile(paths.crunch); err == nil {
		if json.Unmarshal(data, &crunch) != nil {
			return true
		}
		return crunchJobActive(crunch.Status)
	} else if !os.IsNotExist(err) {
		return true
	}
	return false
}

// stopIdleChatDaemon stops the workspace's persistent session when it is idle.
// With suspend, clients that reconnect by themselves may stay attached; they
// are disconnected and reconnect later. It returns nil when no session runs and
// an error naming the reason when the session is busy.
func stopIdleChatDaemon(id string, suspend bool) error {
	running, busy := chatDaemonIdleState(id, suspend)
	if !running {
		return nil
	}
	if busy != "" {
		return errors.New(busy)
	}
	paths, err := chatDaemonPathSet(id)
	if err != nil {
		return err
	}
	workspace, err := workspacePath(id)
	if err != nil {
		return err
	}
	method := "jara/handoff"
	if suspend {
		method = "jara/suspend"
	}
	if err = requestChatDaemonControl(paths, method); err != nil {
		return err
	}
	deadline := time.Now().Add(chatDaemonHandoffTimeout)
	for {
		_, lockErr := os.Lstat(workspace + ".lock")
		if chatDaemonPID(paths) == 0 && os.IsNotExist(lockErr) {
			return nil
		}
		if time.Now().After(deadline) {
			if _, busy = chatDaemonIdleState(id, suspend); busy != "" {
				return errors.New(busy)
			}
			return fmt.Errorf("the persistent Jara session did not stop")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stopIdleChatDaemonsForUpdate runs under the lifecycle lock, so no daemon can
// start between this call and the maintenance lease being written.
func stopIdleChatDaemonsForUpdate() (maintenanceStatus, error) {
	locks, err := filepath.Glob(filepath.Join(fsutil.DataRoot(), "workspaces", "*.lock"))
	if err != nil {
		return maintenanceStatus{}, err
	}
	var blockers updateBlockers
	for _, lock := range locks {
		id, idle := idleChatDaemonWorkspace(lock, true)
		if !idle {
			continue
		}
		if err = stopIdleChatDaemon(id, true); err != nil {
			blockers.add("Jara sessions did not stop", fmt.Sprintf("Jara session in %s did not stop: %v", workspaceBlockerLabel(id), err))
		}
	}
	return blockers.status(), nil
}

// releaseIdleChatDaemon frees a workspace held only by an idle persistent
// session before an operation that must take the workspace lock.
func releaseIdleChatDaemon(id string) error {
	if err := stopIdleChatDaemon(id, false); err != nil {
		return fmt.Errorf("%v; wait for it or stop the session with /stop", err)
	}
	return nil
}

// reconnectingRelayArgs reports whether a command line is a `jara _chat-server`
// relay for a client that reconnects by itself.
func reconnectingRelayArgs(args []string) bool {
	for index := 0; index+2 < len(args); index++ {
		if filepath.Base(args[index]) != "jara" || args[index+1] != "_chat-server" {
			continue
		}
		var request Request
		return decode(args[index+2], &request) == nil && request.Reconnect
	}
	return false
}
