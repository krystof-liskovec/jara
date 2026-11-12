package jara

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func serveChatDaemon(listener net.Listener, server *exec.Cmd, input io.WriteCloser, output io.Reader, paths chatDaemonPaths, schedules *chatScheduleManager, crunch *chatCrunchManager, initial *chatDaemonState) error {
	var inputMu, stateMu, stateWriteMu, pendingMu sync.Mutex
	state := *initial
	pending := map[string]string{}
	requests := newChatDaemonRequestQueue()
	clients := newChatDaemonClientHub(requests)
	router := newChatDaemonClientRouter()
	preserveDetachedIdle := false
	serverDone := make(chan error, 1)
	clientClosed := make(chan chatDaemonClientClosed, 32)
	accepted := make(chan net.Conn, 16)
	idleSignal := make(chan struct{}, 1)
	acceptErrors := make(chan error, 1)
	var shutdown atomic.Bool
	scheduleTicker := time.NewTicker(time.Second)
	defer scheduleTicker.Stop()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, chatDaemonTerminationSignals()...)
	defer signal.Stop(signals)
	stopAccept := make(chan struct{})

	writeState := func() {
		stateWriteMu.Lock()
		stateMu.Lock()
		snapshot := state
		stateMu.Unlock()
		_ = writeChatDaemonState(paths, snapshot)
		stateWriteMu.Unlock()
	}
	setAttachedCount := func() {
		stateWriteMu.Lock()
		stateMu.Lock()
		state.AttachedClients = clients.count()
		state.Attached = state.AttachedClients > 0
		snapshot := state
		stateMu.Unlock()
		_ = writeChatDaemonState(paths, snapshot)
		stateWriteMu.Unlock()
	}
	isDetachedAndIdle := func() bool {
		stateMu.Lock()
		idle := !state.ActiveTurn
		stateMu.Unlock()
		return clients.count() == 0 && idle && len(accepted) == 0 && !preserveDetachedIdle && !schedules.hasPending() && !crunch.hasPending()
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
				_ = conn.Close()
				return
			}
		}
	}()
	go func() {
		scanner := protocol.NewLineScanner(output)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			var message appMessage
			hasMessage := json.Unmarshal(line, &message) == nil
			changed := applyChatDaemonEvent(line, &state, &stateMu, &pending, &pendingMu)
			if changed {
				writeState()
				stateMu.Lock()
				idle := !state.ActiveTurn
				stateMu.Unlock()
				if idle {
					select {
					case idleSignal <- struct{}{}:
					default:
					}
				}
			}
			if hasMessage {
				stateMu.Lock()
				workspace := state.Workspace
				stateMu.Unlock()
				chatPush.observe(message, workspace)
				schedules.handleAppMessage(message)
				isCrunchBusy := func() bool {
					stateMu.Lock()
					busy := state.ActiveTurn || !state.ThreadLoaded
					stateMu.Unlock()
					return busy || schedules.running()
				}
				crunch.handleAppMessage(message, input, &inputMu, isCrunchBusy)
			}
			if hasMessage && len(message.ID) > 0 && message.Method == "" {
				if client, response, ok := router.response(line, message.ID); ok {
					if response != nil {
						_ = client.write(response)
					}
					continue
				}
			}
			clients.broadcast(line, message)
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
			stateMu.Lock()
			ready := remoteChatServerReady{
				CWD: state.CWD, Workspace: state.Workspace, Conversation: state.Conversation,
				Initialized: state.Initialized, ActiveTurn: state.ActiveTurn,
			}
			stateMu.Unlock()
			client, err := clients.attach(conn, ready)
			if err != nil {
				continue
			}
			preserveDetachedIdle = false
			setAttachedCount()
			go readChatDaemonClient(client, input, &inputMu, &state, &stateMu, &stateWriteMu, paths, pending, &pendingMu, router, requests, schedules, crunch, clientClosed)
		case event := <-clientClosed:
			clients.detach(event.client)
			setAttachedCount()
			if event.handoff {
				stateMu.Lock()
				idle := !state.ActiveTurn
				stateMu.Unlock()
				if idle && clients.count() == 0 && !schedules.hasPending() && !crunch.hasPending() {
					shutdown.Store(true)
					break running
				}
			}
			if event.stop {
				_ = schedules.cancelPending()
				crunch.stopAll(input, &inputMu)
				shutdown.Store(true)
				break running
			}
			if event.close && clients.count() == 0 && !schedules.hasPending() && !crunch.hasPending() {
				shutdown.Store(true)
				break running
			}
			stateMu.Lock()
			preserveDetachedIdle = (event.detach || event.disconnected || event.handoff) && clients.count() == 0
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
		case <-scheduleTicker.C:
			isScheduleBusy := func() bool {
				stateMu.Lock()
				busy := state.ActiveTurn || !state.ThreadLoaded
				stateMu.Unlock()
				return busy || crunch.hasPending()
			}
			if err := schedules.startDue(input, &inputMu, isScheduleBusy); err != nil {
				fmt.Fprintf(os.Stderr, "Jara scheduled prompt failed to start: %v\n", err)
			}
			isCrunchBusy := func() bool {
				stateMu.Lock()
				busy := state.ActiveTurn || !state.ThreadLoaded
				stateMu.Unlock()
				return busy || schedules.running()
			}
			if err := crunch.tick(input, &inputMu, isCrunchBusy); err != nil {
				fmt.Fprintf(os.Stderr, "Jara crunch task failed to start: %v\n", err)
			}
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
	clients.closeAll()
	for {
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
			goto drained
		}
	}
drained:
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
	state.AttachedClients = 0
	snapshot := state
	stateMu.Unlock()
	_ = writeChatDaemonState(paths, snapshot)
	stateWriteMu.Unlock()
	return result
}
