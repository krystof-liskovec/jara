package jara

import (
	"encoding/json"
	"fmt"
	"os"
)

type remoteChatServerReady struct {
	CWD            string `json:"cwd"`
	Workspace      string `json:"workspace"`
	Conversation   string `json:"conversation,omitempty"`
	Initialized    bool   `json:"initialized,omitempty"`
	ActiveTurn     bool   `json:"active_turn,omitempty"`
	TurnSelection  bool   `json:"turn_selection,omitempty"`
	Error          string `json:"error,omitempty"`
	ManagedSession bool   `json:"managed_session,omitempty"`
	Notice         string `json:"-"` // where the agent runs, shown after reconnecting
	// Runner is where the agent runs, when a local window learns it while
	// connecting: chatHereRunner, or the host or device it watches.
	Runner string `json:"-"`
}

func runRemoteChatServer(q Request) error {
	if q.UserInterface != "jara" || !nameRE.MatchString(q.ID) {
		return fmt.Errorf("invalid remote Jara workspace request")
	}
	rememberHostName(q.RemoteName)
	if conn, err := hubRunnerRoute(&q); err != nil {
		return err
	} else if conn != nil {
		return proxyChatDaemon(conn)
	}
	if q.Attach {
		// Join the host's running session; never start one.
		if !chatDaemonRunning(q.ID) {
			return fmt.Errorf("no Jara session is running in this workspace")
		}
		conn, err := connectExistingChatDaemon(q.ID)
		if err != nil {
			return err
		}
		return proxyChatDaemon(conn)
	}
	conn, managed, err := ensureChatDaemon(q)
	if err != nil {
		return err
	}
	if managed {
		return sendRemoteChatReady(remoteChatServerReady{ManagedSession: true})
	}
	defer conn.Close()
	return proxyChatDaemon(conn)
}

func sendRemoteChatReady(ready remoteChatServerReady) error {
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"method": "jara/ready",
		"params": ready,
	})
}
