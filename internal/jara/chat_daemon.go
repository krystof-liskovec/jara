package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const chatDaemonStartupTimeout = 3 * time.Minute

type chatDaemonState struct {
	WorkspaceID  string `json:"workspace_id"`
	Workspace    string `json:"workspace"`
	CWD          string `json:"cwd"`
	Conversation string `json:"conversation,omitempty"`
	ThreadLoaded bool   `json:"thread_loaded,omitempty"`
	ActiveTurn   bool   `json:"active_turn,omitempty"`
	Attached     bool   `json:"attached,omitempty"`
	PID          int    `json:"pid"`
	CreatedAt    int64  `json:"created_at"`
}

type chatDaemonPaths struct {
	socket, state, schedules, crunch, pid, start, log string
}

func chatDaemonPathSet(id string) (chatDaemonPaths, error) {
	if !nameRE.MatchString(id) {
		return chatDaemonPaths{}, fmt.Errorf("invalid workspace ID")
	}
	dir := filepath.Join(dataRoot(), "chat")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return chatDaemonPaths{}, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return chatDaemonPaths{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return chatDaemonPaths{}, fmt.Errorf("chat session directory is unsafe")
	}
	base := filepath.Join(dir, id)
	paths := chatDaemonPaths{socket: base + ".sock", state: base + ".json", schedules: base + ".schedules.json", crunch: base + ".crunch.json", pid: base + ".pid", start: base + ".start", log: base + ".log"}
	if len(paths.socket) >= 100 {
		return chatDaemonPaths{}, fmt.Errorf("Jara data path is too long for a local chat socket")
	}
	return paths, nil
}

func chatDaemonPID(paths chatDaemonPaths) int {
	raw, err := os.ReadFile(paths.pid)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid < 2 || !chatProcessExists(pid) {
		_ = os.Remove(paths.pid)
		return 0
	}
	return pid
}

func readChatDaemonState(id string) (chatDaemonState, bool) {
	paths, err := chatDaemonPathSet(id)
	if err != nil {
		return chatDaemonState{}, false
	}
	pid := chatDaemonPID(paths)
	if pid == 0 {
		return chatDaemonState{}, false
	}
	var state chatDaemonState
	data, err := os.ReadFile(paths.state)
	if err != nil || json.Unmarshal(data, &state) != nil || state.WorkspaceID != id || state.PID != pid {
		return chatDaemonState{}, false
	}
	return state, true
}

func chatDaemonRunning(id string) bool {
	paths, err := chatDaemonPathSet(id)
	return err == nil && chatDaemonPID(paths) != 0
}

func validateExistingChatDaemonRequest(request Request) error {
	if request.Worktree {
		return fmt.Errorf("close the persistent Jara session before starting a separate worktree session")
	}
	state, ok := readChatDaemonState(request.ID)
	if request.New && ok && state.ActiveTurn {
		return fmt.Errorf("a turn is already running in this workspace; wait for it to finish before starting another")
	}
	return nil
}

func recoverChatDaemonWorkspaceLock(id string) error {
	workspace, err := workspacePath(id)
	if err != nil {
		return err
	}
	gate, err := lockLifecycle()
	if err != nil {
		return err
	}
	defer gate()
	lock := workspace + ".lock"
	recovered, err := recoverWorkspaceLock(lock)
	if err != nil {
		return err
	}
	if !recovered {
		return fmt.Errorf("workspace operation is active or its lock is too recent to recover: %s", lock)
	}
	return nil
}

func chatDaemonSessions() []Session {
	dir := filepath.Join(dataRoot(), "chat")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Session
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		state, ok := readChatDaemonState(id)
		if !ok {
			continue
		}
		name := "jara-chat-" + strings.TrimPrefix(id, "w-")
		if len(name) > 24 {
			name = name[:24]
		}
		activity := "idle"
		if state.ActiveTurn {
			activity = "running"
		}
		attached := 0
		if state.Attached {
			attached = 1
		}
		out = append(out, Session{Name: name, Workspace: id, ChatDaemon: true, CreatedAt: state.CreatedAt, Conversation: state.Conversation, Attached: attached, AgentState: activity})
	}
	return out
}

func dialChatDaemon(id string) (net.Conn, error) {
	paths, err := chatDaemonPathSet(id)
	if err != nil {
		return nil, err
	}
	return net.DialTimeout("unix", paths.socket, 2*time.Second)
}

func ensureChatDaemon(q Request) (net.Conn, bool, error) {
	paths, err := chatDaemonPathSet(q.ID)
	if err != nil {
		return nil, false, err
	}
	if conn, dialErr := net.DialTimeout("unix", paths.socket, 250*time.Millisecond); dialErr == nil {
		if err = validateExistingChatDaemonRequest(q); err != nil {
			_ = conn.Close()
			return nil, false, err
		}
		return conn, false, nil
	}
	deadline := time.Now().Add(chatDaemonStartupTimeout)
	for time.Now().Before(deadline) {
		if conn, dialErr := net.DialTimeout("unix", paths.socket, 250*time.Millisecond); dialErr == nil {
			if err = validateExistingChatDaemonRequest(q); err != nil {
				_ = conn.Close()
				return nil, false, err
			}
			return conn, false, nil
		}
		pid := chatDaemonPID(paths)
		if pid == 0 {
			if err = os.Mkdir(paths.start, 0700); err == nil {
				conn, managed, startErr := startChatDaemon(q, paths)
				_ = os.Remove(paths.start)
				return conn, managed, startErr
			} else if !os.IsExist(err) {
				return nil, false, err
			} else if info, statErr := os.Stat(paths.start); statErr == nil && time.Since(info.ModTime()) > time.Minute {
				_ = os.Remove(paths.start)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if log, readErr := os.ReadFile(paths.log); readErr == nil && len(log) > 0 {
		return nil, false, fmt.Errorf("remote chat service did not start: %s", strings.TrimSpace(string(log)))
	}
	return nil, false, fmt.Errorf("remote chat service did not become ready")
}

func startChatDaemon(q Request, paths chatDaemonPaths) (net.Conn, bool, error) {
	if err := recoverChatDaemonWorkspaceLock(q.ID); err != nil {
		return nil, false, err
	}
	active, err := activeSessions()
	if err != nil {
		return nil, false, err
	}
	for _, session := range active {
		if session.Workspace == q.ID && !session.ChatDaemon {
			return nil, true, nil
		}
	}
	if q.Conversation == "" && !q.New && !q.Resume && !q.ResumeLatest {
		var previous chatDaemonState
		if data, readErr := os.ReadFile(paths.state); readErr == nil && json.Unmarshal(data, &previous) == nil && previous.WorkspaceID == q.ID {
			q.Conversation = previous.Conversation
		}
	}
	if err = os.Remove(paths.socket); err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}
	if err = os.Remove(paths.log); err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}
	pid, err := startChatDaemonProcess(q, paths.log)
	if err != nil {
		return nil, false, err
	}
	if err = os.WriteFile(paths.pid, []byte(strconv.Itoa(pid)+"\n"), 0600); err != nil {
		return nil, false, err
	}
	deadline := time.Now().Add(chatDaemonStartupTimeout)
	for time.Now().Before(deadline) {
		if conn, dialErr := net.DialTimeout("unix", paths.socket, 250*time.Millisecond); dialErr == nil {
			return conn, false, nil
		}
		if chatDaemonPID(paths) == 0 {
			if log, readErr := os.ReadFile(paths.log); readErr == nil && len(log) > 0 {
				return nil, false, fmt.Errorf("remote chat service failed: %s", strings.TrimSpace(string(log)))
			}
			return nil, false, fmt.Errorf("remote chat service exited during startup")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, false, fmt.Errorf("remote chat service startup timed out")
}

func connectExistingChatDaemon(id string) (net.Conn, error) {
	paths, err := chatDaemonPathSet(id)
	if err != nil {
		return nil, err
	}
	if chatDaemonPID(paths) == 0 {
		return nil, fmt.Errorf("no persistent Jara session is running for workspace %s", id)
	}
	deadline := time.Now().Add(chatDaemonStartupTimeout)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("unix", paths.socket, 500*time.Millisecond)
		if dialErr == nil {
			return conn, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("persistent Jara session did not accept a connection")
}

func proxyChatDaemon(conn net.Conn) error {
	defer conn.Close()
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, os.Stdin)
		done <- err
	}()
	go func() {
		_, err := io.Copy(os.Stdout, conn)
		done <- err
	}()
	err := <-done
	_ = conn.Close()
	other := <-done
	if err != nil && !strings.Contains(err.Error(), "closed network connection") {
		return err
	}
	if other != nil && !strings.Contains(other.Error(), "closed network connection") {
		return other
	}
	return nil
}

func writeChatDaemonReady(conn net.Conn, ready remoteChatServerReady) error {
	return json.NewEncoder(conn).Encode(map[string]any{"method": "jara/ready", "params": ready})
}
