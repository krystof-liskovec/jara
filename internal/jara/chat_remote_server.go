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
	Error          string `json:"error,omitempty"`
	ManagedSession bool   `json:"managed_session,omitempty"`
}

func runRemoteChatServer(q Request) error {
	if q.UserInterface != "jara" || !nameRE.MatchString(q.ID) {
		return fmt.Errorf("invalid remote Jara workspace request")
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
