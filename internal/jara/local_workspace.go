package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

// A folder is a local workspace: Jara runs its agent here without any remote.
// Attaching the folder to a remote later keeps the same session, so its
// conversations, queue and schedules carry over and reach the host with the
// first checkpoint.

// localWorkspaceID names the session of the folder root on this device.
func localWorkspaceID(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return "local-" + hex.EncodeToString(sum[:6])
}

// localSessionID returns the session ID for root. Sessions created before
// folders were keyed by path were named after their remote workspace; their
// files move to the folder's ID, unless that session still runs.
func localSessionID(root, remoteName, workspace string) (string, error) {
	id := localWorkspaceID(root)
	if remoteName == "" || workspace == "" {
		return id, nil
	}
	legacy := localRunnerID(remoteName, workspace)
	if chatDaemonRunning(legacy) {
		return legacy, nil
	}
	if err := adoptLegacySession(legacy, id); err != nil {
		return "", fmt.Errorf("move this folder's earlier session: %w", err)
	}
	return id, nil
}

func adoptLegacySession(legacy, id string) error {
	marker := localRunnerMarkerPath(legacy)
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := os.Stat(localRunnerMarkerPath(id)); err == nil {
		return nil // the folder already has its own session
	}
	from, err := chatDaemonPathSet(legacy)
	if err != nil {
		return err
	}
	to, err := chatDaemonPathSet(id)
	if err != nil {
		return err
	}
	if data, readErr := os.ReadFile(from.state); readErr == nil {
		var state chatDaemonState
		if json.Unmarshal(data, &state) == nil {
			state.WorkspaceID, state.PID = id, 0
			if err = writeChatDaemonState(to, state); err != nil {
				return err
			}
		}
		_ = os.Remove(from.state)
	}
	for _, pair := range [][2]string{{from.schedules, to.schedules}, {from.crunch, to.crunch}, {from.log, to.log}, {marker, localRunnerMarkerPath(id)}} {
		if err = os.Rename(pair[0], pair[1]); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// startLocalOnlyRunner starts the session of a folder without a remote.
func startLocalOnlyRunner(q Request) (*appConnection, remoteChatServerReady, error) {
	applyDaemonResume(&q)
	if err := updateLocalRunnerMarker(q.ID, func(m *localRunnerMarker) { m.Root = q.Root }); err != nil {
		return nil, remoteChatServerReady{}, err
	}
	conn, err := startLocalRunnerDaemon(q)
	if err != nil {
		return nil, remoteChatServerReady{}, err
	}
	return readyLocalRunner(conn, "")
}

// hasUnsentThreads reports whether the host lacks a conversation of root.
func hasUnsentThreads(id, root string) bool {
	known := loadLocalRunnerMarker(id).Versions
	for thread, version := range workspaceThreadVersions(root) {
		if known[thread] != version {
			return true
		}
	}
	return false
}

// localAgentCommand rewrites a container command line to the CLI on this
// device's PATH.
func localAgentCommand(command []string) ([]string, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("no agent command")
	}
	binary, err := exec.LookPath(filepath.Base(command[0]))
	if err != nil {
		return nil, fmt.Errorf("%s is not installed on this device: %w", filepath.Base(command[0]), err)
	}
	return append([]string{binary}, command[1:]...), nil
}

// runLocalNative runs the native Codex or Claude Code terminal here, as if
// the user had started the CLI in this folder.
func runLocalNative(q Request, cwd string) error {
	args := q.CodexArgs
	if q.Runtime != protocol.AgentClaude {
		var err error
		if args, err = configuredCodexArgs(q.CodexArgs); err != nil {
			return err
		}
	}
	if q.Handoff {
		q.Prompt = handoffPrompt(cwd, otherAgent(q.Runtime), q.Runtime, q.Prompt)
	}
	command, err := agentTerminalCommand(q, args)
	if err != nil {
		return err
	}
	if command, err = localAgentCommand(command); err != nil {
		return err
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = cwd, os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func otherAgent(runtime string) string {
	if runtime == protocol.AgentClaude {
		return protocol.AgentCodex
	}
	return protocol.AgentClaude
}

// startLocalBackground is `jara run` without a remote: the prompt runs
// non-interactively in a detached process that logs its output.
func startLocalBackground(q Request, cwd string) error {
	dir := filepath.Join(fsutil.DataRoot(), "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	logPath := filepath.Join(dir, time.Now().Format("20060102-150405.000")+".log")
	q.Root = cwd
	if _, err := startDetachedJara("_local-run", q, logPath); err != nil {
		return err
	}
	fmt.Printf("Running in the background; output: %s\n", logPath)
	return nil
}

// runLocalBackground is `jara _local-run REQUEST`, started by
// startLocalBackground.
func runLocalBackground(q Request) error {
	if !q.Background || q.Prompt == "" || q.Root == "" {
		return fmt.Errorf("invalid background run")
	}
	var command []string
	if q.Runtime == protocol.AgentClaude {
		command = backgroundClaudeCommand(q)
	} else {
		args, err := configuredCodexArgs(q.CodexArgs)
		if err != nil {
			return err
		}
		q.CodexArgs = args
		command = backgroundCodexCommand(q)
	}
	command, err := localAgentCommand(command)
	if err != nil {
		return err
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = q.Root, os.Stdout, os.Stderr
	if q.Runtime == protocol.AgentClaude {
		cmd.Stdin = strings.NewReader(q.Prompt)
	}
	return cmd.Run()
}
