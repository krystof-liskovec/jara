package jara

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentrouter"
)

// runLocalRunnerDaemon is `jara _runner-daemon`: the persistent conversation
// service of a workspace whose agent runs on this device. It is the remote
// host's chat daemon, run against the local checkout with the local agent
// CLIs, plus a checkpoint after every turn and a tunnel that lets other
// devices attach through the remote host.
func runLocalRunnerDaemon(q Request) error {
	if q.UserInterface != "jara" || q.Runner == "" || q.Root == "" || !nameRE.MatchString(q.Workspace) {
		return fmt.Errorf("invalid local runner request")
	}
	paths, err := chatDaemonPathSet(q.ID)
	if err != nil {
		return err
	}
	if err = waitForChatDaemonPID(paths); err != nil {
		return err
	}
	defer removeChatDaemonPID(paths, os.Getpid())
	config, err := readConfig()
	if err != nil {
		return err
	}
	remote, ok := config.Remotes[q.RemoteName]
	if !ok {
		return fmt.Errorf("remote %s is not configured", q.RemoteName)
	}
	cwd := filepath.Join(q.Root, filepath.FromSlash(q.Subdir))
	if !pathWithin(q.Root, cwd) {
		return fmt.Errorf("working directory is outside the checkout")
	}
	server, err := localRunnerAgentServer(q, cwd)
	if err != nil {
		return err
	}
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
		return fmt.Errorf("start local Codex app-server: %w", err)
	}
	stopServer := func() { input.Close(); killPersistentChatProcess(server); _ = server.Wait() }
	_ = os.Remove(paths.socket)
	listener, err := net.Listen("unix", paths.socket)
	if err != nil {
		stopServer()
		return err
	}
	defer os.Remove(paths.socket)
	if err = os.Chmod(paths.socket, 0600); err != nil {
		listener.Close()
		stopServer()
		return err
	}
	schedules, err := newChatScheduleManager(paths.schedules)
	if err != nil {
		listener.Close()
		stopServer()
		return fmt.Errorf("load scheduled prompts: %w", err)
	}
	crunch, err := newChatCrunchManager(paths.crunch)
	if err != nil {
		listener.Close()
		stopServer()
		return fmt.Errorf("load crunch task: %w", err)
	}
	state := chatDaemonState{WorkspaceID: q.ID, Workspace: filepath.Base(q.Root), CWD: cwd, Conversation: q.Conversation, PID: os.Getpid(), CreatedAt: time.Now().UnixNano()}
	if err = writeChatDaemonState(paths, state); err != nil {
		listener.Close()
		stopServer()
		return err
	}
	current := func() chatDaemonState {
		saved, _, _ := readSavedChatDaemonState(q.ID, paths)
		return saved
	}
	status := func() runnerHeartbeat {
		saved := current()
		return runnerHeartbeat{Busy: saved.ActiveTurn, Pending: schedules.hasPending() || crunch.hasPending(), Conversation: saved.Conversation}
	}
	runner := newLocalRunner(remote, q, paths, status)
	go runner.linkLoop(paths.socket)
	go runner.midTurnCheckpoints(current)
	go watchSleep(runner.stop, func() { runner.goingToSleep(current(), sleepBudget) })
	result := serveChatDaemon(listener, server, input, output, paths, schedules, crunch, &state, runner.hooks())
	runner.stopLink()
	if !runner.isLost() && !runner.released {
		// The session ended here (for example /quit): save the final state and
		// free the workspace so any device, or the remote host, continues it.
		final, _, _ := readSavedChatDaemonState(q.ID, paths)
		if err = runner.handOver(final); err != nil {
			fmt.Fprintln(os.Stderr, "The workspace stays assigned to this device:", err)
		}
	}
	return result
}

// localRunnerAgentServer starts Jara's agent router over the local Codex
// app-server, with the local Claude Code CLI when installed.
func localRunnerAgentServer(q Request, cwd string) (*exec.Cmd, error) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("running the agent on this device needs the Codex CLI on PATH: %w", err)
	}
	codexArgs, err := configuredCodexArgs(q.CodexArgs)
	if err != nil {
		return nil, err
	}
	runtime := codexChatRuntime
	runtime.binary = codex
	binary, args, err := runtime.command(codexArgs)
	if err != nil {
		return nil, err
	}
	binary, args = agentServerCommand(binary, args, q.Runtime, q.AgentExplicit, q.AgentArgs)
	server := exec.Command(binary, args...)
	server.Dir = cwd
	server.Env = append(os.Environ(), agentrouter.PortableThreadsEnv+"=1")
	if os.Getenv("JARA_CLAUDE_BIN") == "" {
		if claude, lookErr := exec.LookPath("claude"); lookErr == nil {
			server.Env = append(server.Env, "JARA_CLAUDE_BIN="+claude)
		}
	}
	isolatePersistentChatProcess(server)
	return server, nil
}

// startLocalRunnerDaemon starts `jara _runner-daemon` and connects to it.
func startLocalRunnerDaemon(q Request) (net.Conn, error) {
	paths, err := chatDaemonPathSet(q.ID)
	if err != nil {
		return nil, err
	}
	if conn, dialErr := net.DialTimeout("unix", paths.socket, 250*time.Millisecond); dialErr == nil {
		return conn, nil
	}
	_ = os.Remove(paths.socket)
	pid, err := startChatDaemonProcess(q, paths.log)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(paths.pid, []byte(fmt.Sprintf("%d\n", pid)), 0600); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(chatDaemonStartupTimeout)
	for time.Now().Before(deadline) {
		if conn, dialErr := net.DialTimeout("unix", paths.socket, 250*time.Millisecond); dialErr == nil {
			return conn, nil
		}
		if chatDaemonPID(paths) == 0 {
			log, _ := os.ReadFile(paths.log)
			return nil, fmt.Errorf("local agent session failed to start: %s", strings.TrimSpace(string(log)))
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("local agent session startup timed out")
}
