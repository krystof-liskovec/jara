package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

type chatDiagnosticBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *chatDiagnosticBuffer) Write(data []byte) (int, error) {
	const limit = 32 * 1024
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, data...)
	if len(b.data) > limit {
		b.data = append([]byte(nil), b.data[len(b.data)-limit:]...)
	}
	return len(data), nil
}

func (b *chatDiagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}

type chatLoopOptions struct {
	cwd, id, workspaceLabel string
	trackAgentState         bool
	persistentSession       bool
	initialized             bool
	attachedTurnActive      bool
	resumeAtStart           bool
	resumeLatest            bool
	reconnect               chatReconnector // nil when a lost connection ends the interface
}

func chatLoop(c *appConnection, input io.Reader, output io.Writer, cwd, id string) error {
	return chatLoopWithOptions(c, input, output, chatLoopOptions{cwd: cwd, id: id, trackAgentState: true})
}

func chatLoopWithOptions(c *appConnection, input io.Reader, output io.Writer, options chatLoopOptions) error {
	u := &chatUI{
		c: c, out: output, cwd: options.cwd, thread: options.id, busy: true, startedAt: time.Now(),
		workspaceLabel: options.workspaceLabel, trackAgentState: options.trackAgentState,
		persistentSession:  options.persistentSession,
		connected:          options.initialized,
		attachedTurnActive: options.attachedTurnActive,
		resumeAtStart:      options.resumeAtStart, resumeLatest: options.resumeLatest,
		pending: map[string]chatPending{}, pendingTurnModels: map[string]chatTurnRequest{},
		items: map[string]json.RawMessage{},
	}
	u.reconnect = options.reconnect
	defer func() {
		u.stopReconnecting()
		if u.c != c {
			u.c.close() // the caller closes the connection it passed in
		}
	}()
	if u.trackAgentState {
		defer clearChatAgentState()
	}
	if terminalIn, terminalOut, ok := chatInteractiveFiles(input, output); ok {
		return chatTerminalLoop(u, terminalIn, terminalOut)
	}
	return chatPlainLoop(u, input, output)
}

func chatPlainLoop(u *chatUI, input io.Reader, output io.Writer) error {
	u.out = output
	u.setAgentState("starting")
	u.print("Jara conversation · /help for commands · Ctrl-C cancels a turn; while idle it stops the session\n")
	if err := u.initializeChatServer(); err != nil {
		return err
	}
	lines := make(chan string)
	inputErrors := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		scanner := protocol.NewLineScanner(input)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-stop:
				return
			}
		}
		inputErrors <- scanner.Err()
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case message := <-u.c.messages:
			if err := u.event(message); err != nil {
				return err
			}
		case err := <-u.c.errors:
			if !u.connectionLost(err) {
				return fmt.Errorf("Codex app-server disconnected: %w", err)
			}
		case result := <-u.reconnectRuns:
			if err := u.acceptReconnect(result); err != nil {
				return err
			}
		case line := <-lines:
			quit, err := u.line(line)
			if err != nil {
				return err
			}
			if quit {
				return nil
			}
		case err := <-inputErrors:
			u.setAgentState("idle")
			return err
		case <-signals:
			if u.agentTurnActive() || (u.busy && !u.persistentSession) {
				if err := u.interrupt(); err != nil {
					return err
				}
			} else {
				if err := u.stopIdleSession(); err != nil {
					return err
				}
				return nil
			}
		case now := <-timer.C:
			for id, pending := range u.pending {
				if now.After(pending.deadline) {
					if err := u.expireChatRequest(id, pending); err != nil {
						return err
					}
				}
			}
		}
	}
}
