package jara

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

func runChatDaemon(q Request) error {
	if q.UserInterface != "jara" || !nameRE.MatchString(q.ID) {
		return fmt.Errorf("invalid persistent Jara session request")
	}
	paths, err := chatDaemonPathSet(q.ID)
	if err != nil {
		return err
	}
	if err = waitForChatDaemonPID(paths); err != nil {
		return err
	}
	defer removeChatDaemonPID(paths, os.Getpid())
	unlock, err := lockWorkspace(q.ID)
	if err != nil {
		return err
	}
	defer unlock()
	lockOwner, err := markChatDaemonWorkspaceLock(q.ID)
	if err != nil {
		return err
	}
	defer os.Remove(lockOwner)
	workspace, err := workspaceInfo(q.ID)
	if err != nil {
		return err
	}
	if !workspace.Ready {
		return fmt.Errorf("workspace initialization is unfinished; retry the original local --clone/--copy command")
	}
	expected, _ := workspacePath(q.ID)
	if filepath.Clean(workspace.Path) != filepath.Clean(expected) {
		return fmt.Errorf("workspace metadata path does not match its ID")
	}
	if info, statErr := os.Lstat(workspace.Path); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("remote workspace is unavailable: %s", workspace.Path)
	}
	active, err := activeSessions()
	if err != nil {
		return err
	}
	for _, session := range active {
		if session.Workspace == q.ID && !session.ChatDaemon {
			return fmt.Errorf("workspace already has a remote-managed session")
		}
	}
	cwd, err := chatDaemonWorkingDirectory(q, workspace)
	if err != nil {
		return err
	}
	if err = prepareToolsQuiet(cwd); err != nil {
		return err
	}
	codexArgs, err := configuredCodexArgs(q.CodexArgs)
	if err != nil {
		return err
	}
	provider, err := resolveChatRuntime(defaultChatRuntime)
	if err != nil {
		return err
	}
	binary, args, err := provider.command(codexArgs)
	if err != nil {
		return err
	}
	server := projectToolCommand(cwd, append([]string{binary}, args...)...)
	isolatePersistentChatProcess(server)
	input, err := server.StdinPipe()
	if err != nil {
		return err
	}
	output, err := server.StdoutPipe()
	if err != nil {
		input.Close()
		return err
	}
	server.Stderr = os.Stderr
	if err = server.Start(); err != nil {
		input.Close()
		return fmt.Errorf("start remote Codex app-server: %w", err)
	}
	listener, err := net.Listen("unix", paths.socket)
	if err != nil {
		input.Close()
		killPersistentChatProcess(server)
		_ = server.Wait()
		return err
	}
	if err = os.Chmod(paths.socket, 0600); err != nil {
		listener.Close()
		input.Close()
		killPersistentChatProcess(server)
		_ = server.Wait()
		return err
	}
	defer listener.Close()
	defer os.Remove(paths.socket)
	state := chatDaemonState{WorkspaceID: q.ID, Workspace: workspace.Name, CWD: cwd, Conversation: q.Conversation, PID: os.Getpid(), CreatedAt: time.Now().UnixNano()}
	if err = writeChatDaemonState(paths, state); err != nil {
		input.Close()
		killPersistentChatProcess(server)
		_ = server.Wait()
		return err
	}
	return serveChatDaemon(listener, server, input, output, paths, &state)
}

func waitForChatDaemonPID(paths chatDaemonPaths) error {
	defer os.Remove(paths.start)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if chatDaemonPID(paths) == os.Getpid() {
			_ = os.Remove(paths.start)
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("persistent Jara session launcher did not register its process")
}

func markChatDaemonWorkspaceLock(id string) (string, error) {
	workspace, err := workspacePath(id)
	if err != nil {
		return "", err
	}
	path := filepath.Join(workspace+".lock", "chat-daemon.pid")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if _, err = fmt.Fprintln(file, os.Getpid()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func chatDaemonWorkingDirectory(q Request, workspace Workspace) (string, error) {
	cwd := workspace.Path
	if q.Worktree {
		name := tmuxSessionName(workspace, q.ID)
		cwd = filepath.Join(dataRoot(), "worktrees", name)
		if err := os.MkdirAll(filepath.Dir(cwd), 0700); err != nil {
			return "", err
		}
		if err := runGit(workspace.Path, "worktree", "add", "-b", name, cwd, "HEAD"); err != nil {
			return "", fmt.Errorf("create remote worktree: %w", err)
		}
	}
	if q.Subdir != "" && q.Subdir != "." {
		var err error
		cwd, err = safePath(cwd, q.Subdir)
		if err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(cwd)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("remote working directory is unavailable: %s", cwd)
	}
	return cwd, nil
}

func serveChatDaemon(listener net.Listener, server *exec.Cmd, input io.WriteCloser, output io.Reader, paths chatDaemonPaths, initial *chatDaemonState) error {
	var currentMu sync.Mutex
	var current net.Conn
	preserveDetachedIdle := false
	var inputMu sync.Mutex
	var stateMu sync.Mutex
	var stateWriteMu sync.Mutex
	var pendingMu sync.Mutex
	serverRequests := newChatDaemonRequestQueue()
	state := *initial
	pending := map[string]string{}
	serverDone := make(chan error, 1)
	clientClosed := make(chan chatDaemonClientClosed, 1)
	accepted := make(chan net.Conn, 1)
	idleSignal := make(chan struct{}, 1)
	acceptErrors := make(chan error, 1)
	var shutdown atomic.Bool
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, chatDaemonTerminationSignals()...)
	defer signal.Stop(signals)
	stopAccept := make(chan struct{})
	isDetachedAndIdle := func() bool {
		currentMu.Lock()
		detached := current == nil
		currentMu.Unlock()
		stateMu.Lock()
		idle := !state.ActiveTurn
		stateMu.Unlock()
		return detached && idle && len(accepted) == 0 && !preserveDetachedIdle
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				acceptErrors <- err
				return
			}
			select {
			case accepted <- conn:
			case <-stopAccept:
				conn.Close()
				return
			}
		}
	}()
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			changed := applyChatDaemonEvent(line, &state, &stateMu, &pending, &pendingMu)
			if changed {
				stateWriteMu.Lock()
				stateMu.Lock()
				snapshot := state
				stateMu.Unlock()
				_ = writeChatDaemonState(paths, snapshot)
				stateWriteMu.Unlock()
				if !snapshot.ActiveTurn {
					select {
					case idleSignal <- struct{}{}:
					default:
					}
				}
			}
			currentMu.Lock()
			var message appMessage
			if json.Unmarshal(line, &message) == nil && len(message.ID) > 0 && message.Method != "" {
				serverRequests.add(message.ID, line)
			}
			conn := current
			if conn != nil {
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				err := writeChatDaemonLine(conn, line)
				if err != nil {
					_ = conn.Close()
					current = nil
				}
			}
			currentMu.Unlock()
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		if waitErr := server.Wait(); err == io.EOF {
			err = waitErr
		}
		serverDone <- err
	}()

	var result error
	serverExited := false
running:
	for {
		select {
		case conn := <-accepted:
			currentMu.Lock()
			if current != nil {
				_ = writeChatDaemonError(conn, "Jara session already has an attached client")
				_ = conn.Close()
				currentMu.Unlock()
				continue
			}
			preserveDetachedIdle = false
			stateWriteMu.Lock()
			stateMu.Lock()
			state.Attached = true
			ready := remoteChatServerReady{CWD: state.CWD, Workspace: state.Workspace, Conversation: state.Conversation, ActiveTurn: state.ActiveTurn}
			snapshot := state
			stateMu.Unlock()
			_ = writeChatDaemonState(paths, snapshot)
			stateWriteMu.Unlock()
			if err := writeChatDaemonReady(conn, ready); err != nil {
				_ = conn.Close()
				stateWriteMu.Lock()
				stateMu.Lock()
				state.Attached = false
				snapshot = state
				stateMu.Unlock()
				_ = writeChatDaemonState(paths, snapshot)
				stateWriteMu.Unlock()
				currentMu.Unlock()
				continue
			}
			current = conn
			if err := serverRequests.writePending(conn); err != nil {
				current = nil
				_ = conn.Close()
				stateWriteMu.Lock()
				stateMu.Lock()
				state.Attached = false
				snapshot = state
				stateMu.Unlock()
				_ = writeChatDaemonState(paths, snapshot)
				stateWriteMu.Unlock()
				currentMu.Unlock()
				continue
			}
			currentMu.Unlock()
			go readChatDaemonClient(conn, input, &inputMu, &state, &stateMu, &stateWriteMu, paths, pending, &pendingMu, serverRequests, clientClosed)
		case event := <-clientClosed:
			currentMu.Lock()
			if current == event.conn {
				current = nil
			}
			currentMu.Unlock()
			_ = event.conn.Close()
			stateWriteMu.Lock()
			stateMu.Lock()
			state.Attached = false
			snapshot := state
			stateMu.Unlock()
			_ = writeChatDaemonState(paths, snapshot)
			stateWriteMu.Unlock()
			if event.close {
				shutdown.Store(true)
				break running
			}
			stateMu.Lock()
			preserveDetachedIdle = event.detach && !state.ActiveTurn
			stateMu.Unlock()
			if !event.detach && isDetachedAndIdle() {
				shutdown.Store(true)
				break running
			}
		case <-idleSignal:
			if isDetachedAndIdle() {
				shutdown.Store(true)
				break running
			}
		case err := <-serverDone:
			serverExited = true
			if err != nil && !errors.Is(err, io.EOF) {
				result = fmt.Errorf("Codex app-server stopped: %w", err)
			}
			break running
		case err := <-acceptErrors:
			if err != nil && !shutdown.Load() {
				result = fmt.Errorf("chat session listener stopped: %w", err)
			}
			break running
		case <-signals:
			shutdown.Store(true)
			break running
		}
	}

	close(stopAccept)
	_ = listener.Close()
	currentMu.Lock()
	if current != nil {
		_ = current.Close()
		current = nil
	}
	currentMu.Unlock()
	_ = input.Close()
	if !serverExited {
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			killPersistentChatProcess(server)
			<-serverDone
		}
	}
	stateWriteMu.Lock()
	stateMu.Lock()
	state.ActiveTurn = false
	state.Attached = false
	snapshot := state
	stateMu.Unlock()
	_ = writeChatDaemonState(paths, snapshot)
	stateWriteMu.Unlock()
	return result
}
