package jara

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	schedules, err := newChatScheduleManager(paths.schedules)
	if err != nil {
		return fmt.Errorf("load scheduled prompts: %w", err)
	}
	crunch, err := newChatCrunchManager(paths.crunch)
	if err != nil {
		return fmt.Errorf("load crunch task: %w", err)
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
	return serveChatDaemon(listener, server, input, output, paths, schedules, crunch, &state)
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
