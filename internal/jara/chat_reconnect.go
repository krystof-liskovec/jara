package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// A local client reaches its persistent session through an SSH relay. When
// that connection drops (a network change, or an update stopping an idle
// session), the interface reconnects with backoff instead of exiting, then
// resumes the conversation. Prompts sent meanwhile wait in the queue.

// chatReconnector opens a new connection to the session, resuming thread.
type chatReconnector func(thread string) (*appConnection, remoteChatServerReady, error)

type chatReconnectResult struct {
	conn  *appConnection
	ready remoteChatServerReady
	err   error
	retry time.Duration // delay before the next attempt after err
}

type chatReconnectState struct {
	reconnect     chatReconnector
	reconnectRuns chan chatReconnectResult // nil while connected
	reconnectStop chan struct{}
	sessionEnded  bool // the session stopped on purpose; do not restart it
}

var chatReconnectDelays = []time.Duration{
	500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second,
}

// offlineChatConnection stands in while reconnecting: writes are discarded
// and it never delivers messages or errors.
func offlineChatConnection() *appConnection {
	return &appConnection{
		input: chatNopWriteCloser{io.Discard}, encoder: json.NewEncoder(io.Discard),
		messages: make(chan appMessage), errors: make(chan error), stop: make(chan struct{}),
	}
}

type chatNopWriteCloser struct{ io.Writer }

func (chatNopWriteCloser) Close() error { return nil }

func (u *chatUI) chatOffline() bool { return u.reconnectRuns != nil }

// connectionLost starts reconnecting and reports whether the interface should
// keep running.
func (u *chatUI) connectionLost(cause error) bool {
	if u.reconnect == nil {
		return false
	}
	// The reader queues every message before reporting the error, so the
	// session's final messages, such as jara/session/ended, are all here.
	for drained := false; !drained; {
		select {
		case message := <-u.c.messages:
			_ = u.event(message)
		default:
			drained = true
		}
	}
	if u.sessionEnded {
		return false
	}
	old := u.c
	go old.close()
	u.c = offlineChatConnection()
	u.pending = map[string]chatPending{}
	u.pendingTurnModels = map[string]chatTurnRequest{}
	u.items = map[string]json.RawMessage{}
	u.chatTurnState = chatTurnState{busy: true}
	u.setActivityLabel("Reconnecting")
	u.setAgentState("starting")
	u.printRole("system", "Connection to the Jara session was lost ("+chatText(cause.Error())+"). Reconnecting; prompts you send wait in the queue. /quit stops waiting.\n")
	u.reconnectRuns = make(chan chatReconnectResult, 1)
	u.reconnectStop = make(chan struct{})
	go runChatReconnect(u.reconnect, u.thread, u.reconnectRuns, u.reconnectStop)
	return true
}

func runChatReconnect(reconnect chatReconnector, thread string, results chan<- chatReconnectResult, stop <-chan struct{}) {
	for attempt := 0; ; attempt++ {
		delay := chatReconnectDelays[min(attempt, len(chatReconnectDelays)-1)]
		select {
		case <-stop:
			return
		case <-time.After(delay):
		}
		conn, ready, err := reconnect(thread)
		result := chatReconnectResult{conn: conn, ready: ready, err: err}
		if err != nil {
			result.retry = chatReconnectDelays[min(attempt+1, len(chatReconnectDelays)-1)]
		}
		select {
		case results <- result:
		case <-stop:
			if conn != nil {
				conn.close()
			}
			return
		}
		if err == nil {
			return
		}
	}
}

// acceptReconnect applies one reconnect attempt.
func (u *chatUI) acceptReconnect(result chatReconnectResult) error {
	if result.err != nil {
		u.setActivityLabel(fmt.Sprintf("Reconnecting (retry in %s)", result.retry.Round(time.Second)))
		return nil
	}
	u.reconnectRuns, u.reconnectStop = nil, nil
	u.c = result.conn
	u.connected = result.ready.Initialized
	u.attachedTurnActive = result.ready.ActiveTurn
	u.resumeAtStart = false
	if u.thread == "" {
		u.thread = result.ready.Conversation
	}
	// Resuming reloads the whole history, so start from an empty transcript.
	if transcript, ok := u.out.(interface{ ClearChat() }); ok {
		transcript.ClearChat()
	}
	u.afterHistory = "Reconnected to the Jara session.\n"
	return u.initializeChatServer()
}

// stopReconnecting ends a reconnect loop when the interface exits.
func (u *chatUI) stopReconnecting() {
	if u.reconnectStop != nil {
		close(u.reconnectStop)
		u.reconnectRuns, u.reconnectStop = nil, nil
	}
}
