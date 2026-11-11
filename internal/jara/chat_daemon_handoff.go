package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const chatDaemonHandoffTimeout = 8 * time.Second

// prepareChatDaemonForCodex stops an idle Jara daemon and returns its saved
// conversation metadata so a regular Codex terminal can resume that thread.
func prepareChatDaemonForCodex(id string) (chatDaemonState, bool, error) {
	paths, err := chatDaemonPathSet(id)
	if err != nil {
		return chatDaemonState{}, false, err
	}
	state, hasState, err := readSavedChatDaemonState(id, paths)
	if err != nil {
		return chatDaemonState{}, false, err
	}
	if chatDaemonPID(paths) == 0 {
		if !hasState {
			return chatDaemonState{}, false, nil
		}
		if err = recoverChatDaemonWorkspaceMetadata(id, paths); err != nil {
			return state, true, fmt.Errorf("recover persistent Jara workspace: %w", err)
		}
		return state, true, nil
	}
	if !hasState {
		return chatDaemonState{}, false, fmt.Errorf("persistent Jara session state is unreadable; reconnect with `jara connect --interface jara %s`", id)
	}
	if state.ActiveTurn {
		return state, true, fmt.Errorf("a Jara turn is still running; let it finish before switching to the Codex interface")
	}
	clients := state.AttachedClients
	if clients == 0 && state.Attached {
		clients = 1
	}
	if clients != 0 {
		return state, true, fmt.Errorf("another client is attached to the persistent Jara session; detach it before switching to Codex")
	}
	conn, err := net.DialTimeout("unix", paths.socket, 2*time.Second)
	if err != nil {
		return state, true, fmt.Errorf("persistent Jara session is not accepting a handoff: %w", err)
	}
	// The daemon greets every client with jara/ready before it reads; a client
	// that hangs up first makes that write fail and the handoff is never seen.
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = bufio.NewReader(conn).ReadBytes('\n'); err != nil {
		_ = conn.Close()
		return state, true, fmt.Errorf("persistent Jara session did not answer the handoff: %w", err)
	}
	if err = json.NewEncoder(conn).Encode(map[string]string{"method": "jara/handoff"}); err != nil {
		_ = conn.Close()
		return state, true, fmt.Errorf("request Codex handoff: %w", err)
	}
	_ = conn.Close()
	deadline := time.Now().Add(chatDaemonHandoffTimeout)
	for time.Now().Before(deadline) {
		if chatDaemonPID(paths) == 0 {
			if err = recoverChatDaemonWorkspaceMetadata(id, paths); err != nil {
				return state, true, fmt.Errorf("recover persistent Jara workspace: %w", err)
			}
			return state, true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	latest, ok := readChatDaemonState(id)
	if ok && latest.ActiveTurn {
		return latest, true, fmt.Errorf("a Jara turn started during the handoff; the persistent session is still running")
	}
	return state, true, fmt.Errorf("persistent Jara session did not stop; it may have attached clients or scheduled work. Reconnect with `jara connect --interface jara %s`", id)
}

func readSavedChatDaemonState(id string, paths chatDaemonPaths) (chatDaemonState, bool, error) {
	data, err := os.ReadFile(paths.state)
	if os.IsNotExist(err) {
		return chatDaemonState{}, false, nil
	}
	if err != nil {
		return chatDaemonState{}, false, err
	}
	var state chatDaemonState
	if err = json.Unmarshal(data, &state); err != nil || state.WorkspaceID != id {
		return chatDaemonState{}, false, nil
	}
	state.CWD = strings.TrimSpace(state.CWD)
	return state, true, nil
}
