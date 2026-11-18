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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func serveChatDaemon(listener net.Listener, server *exec.Cmd, input io.WriteCloser, output io.Reader, paths chatDaemonPaths, schedules *chatScheduleManager, crunch *chatCrunchManager, initial *chatDaemonState, hooks ...chatDaemonRunnerHooks) error {
	var runner chatDaemonRunnerHooks
	if len(hooks) > 0 {
		runner = hooks[0]
	}
	var inputMu, stateMu, stateWriteMu, pendingMu sync.Mutex
	state := *initial
	pending := map[string]string{}
	requests := newChatDaemonRequestQueue()
	clients := newChatDaemonClientHub(requests)
	router := newChatDaemonClientRouter()
	preserveDetachedIdle := false
	releaseWanted := false // a device asked for the workspace while a turn ran
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
	schedules.onQueue = func(line []byte) { clients.broadcast(line, appMessage{Method: "jara/queue/updated"}) }
	if runner.bootstrap != "" {
		// No window opened this session: initialize the agent and load the
		// conversation so scheduled and continued turns can start.
		bootstrapChatDaemon(input, runner.bootstrap, &state, pending)
	}

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
		state.ReconnectingClients = clients.reconnecting()
		state.Attached = state.AttachedClients > 0
		snapshot := state
		stateMu.Unlock()
		_ = writeChatDaemonState(paths, snapshot)
		stateWriteMu.Unlock()
	}
	compactor := newChatIdleCompactor()
	compactor.begin = func(requestID string, notice []byte) {
		pendingMu.Lock()
		pending[strconv.Quote(requestID)] = "thread/compact/start"
		pendingMu.Unlock()
		stateMu.Lock()
		state.ActiveTurn = true
		stateMu.Unlock()
		writeState()
		clients.broadcast(notice, appMessage{Method: "jara/idleCompaction/started"})
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
			stateMu.Lock()
			wasActive := state.ActiveTurn
			stateMu.Unlock()
			changed := applyChatDaemonEvent(line, &state, &stateMu, &pending, &pendingMu)
			if changed {
				writeState()
				stateMu.Lock()
				idle := !state.ActiveTurn
				snapshot := state
				stateMu.Unlock()
				if idle && wasActive && runner.turnEnded != nil {
					runner.turnEnded(snapshot)
				}
				if idle {
					select {
					case idleSignal <- struct{}{}:
					default:
					}
				}
			}
			if hasMessage {
				stateMu.Lock()
				workspace, conversation := state.Workspace, state.Conversation
				stateMu.Unlock()
				chatPush.observe(message, workspace)
				compactor.observe(message, conversation)
				schedules.handleAppMessage(message)
				isCrunchBusy := func() bool {
					stateMu.Lock()
					busy := state.ActiveTurn || !state.ThreadLoaded
					stateMu.Unlock()
					return busy || schedules.running() || compactor.running()
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
	reconnect := false // tells attached clients whether to reconnect after shutdown
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
			if queued := schedules.queuedPrompts(); len(queued) > 0 {
				_ = client.write(chatQueueNotification(queued))
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
			if event.suspend {
				// Updates may stop a session whose remaining clients all
				// reconnect by themselves; closing them starts that reconnect.
				stateMu.Lock()
				idle := !state.ActiveTurn
				stateMu.Unlock()
				if idle && clients.count() == clients.reconnecting() && !schedules.hasPending() && !crunch.hasPending() {
					reconnect = true
					shutdown.Store(true)
					break running
				}
			}
			if event.release && runner.release != nil {
				releaseWanted = true // handed over on the next tick when idle
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
			preserveDetachedIdle = (event.detach || event.disconnected || event.handoff || event.suspend) && clients.count() == 0
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
			stateMu.Lock()
			snapshot := state
			stateMu.Unlock()
			// Hand the workspace to the device that asked for it once the
			// running turn is done; attached clients reconnect to it.
			if !snapshot.ActiveTurn && !schedules.running() && !crunch.hasPending() {
				// One attempt per request: a failed release (it could not save
				// its checkpoint) leaves the claimant waiting, not a busy loop.
				release := releaseWanted && runner.release(snapshot)
				releaseWanted = false
				if release || runner.handBack != nil && runner.handBack() {
					reconnect = true
					shutdown.Store(true)
					break running
				}
			}
			isScheduleBusy := func() bool {
				stateMu.Lock()
				busy := state.ActiveTurn || !state.ThreadLoaded
				stateMu.Unlock()
				return busy || crunch.hasPending() || compactor.running()
			}
			if err := schedules.startDue(input, &inputMu, isScheduleBusy); err != nil {
				fmt.Fprintf(os.Stderr, "Jara scheduled prompt failed to start: %v\n", err)
			}
			isCrunchBusy := func() bool {
				stateMu.Lock()
				busy := state.ActiveTurn || !state.ThreadLoaded
				stateMu.Unlock()
				return busy || schedules.running() || compactor.running()
			}
			if err := crunch.tick(input, &inputMu, isCrunchBusy); err != nil {
				fmt.Fprintf(os.Stderr, "Jara crunch task failed to start: %v\n", err)
			}
			isCompactBusy := func() bool {
				stateMu.Lock()
				busy := state.ActiveTurn || !state.ThreadLoaded
				stateMu.Unlock()
				return busy || schedules.running() || crunch.hasPending()
			}
			if err := compactor.tick(input, &inputMu, snapshot.Agent, isCompactBusy); err != nil {
				fmt.Fprintf(os.Stderr, "Jara idle compaction failed to start: %v\n", err)
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
		case <-runner.lost:
			// Attached windows reconnect and follow the conversation to the
			// machine that continues it.
			fmt.Fprintln(os.Stderr, "The conversation continues on another machine; stopping the local agent.")
			reconnect = true
			shutdown.Store(true)
			break running
		case <-signals:
			reconnect = true
			shutdown.Store(true)
			break running
		}
	}

	close(stopAccept)
	_ = listener.Close()
	if ended, err := json.Marshal(map[string]any{"method": "jara/session/ended", "params": map[string]bool{"reconnect": reconnect}}); err == nil {
		clients.broadcast(ended, appMessage{Method: "jara/session/ended"})
	}
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

// chatDaemonRunnerHooks connect a daemon running on a local device to the
// remote host that keeps the shared copy of its workspace and conversation.
type chatDaemonRunnerHooks struct {
	turnEnded func(chatDaemonState)      // a turn completed; checkpoint it
	release   func(chatDaemonState) bool // final checkpoint and lease release
	lost      <-chan struct{}            // closed when another machine took over
	handBack  func() bool                // the host: a device waits for the workspace
	bootstrap string                     // conversation to load without a window
}

// bootstrapChatDaemon starts a session no window has opened yet. Responses
// to these requests update the daemon state like a window's would.
func bootstrapChatDaemon(input io.Writer, thread string, state *chatDaemonState, pending map[string]string) {
	resume := map[string]any{"threadId": thread, "approvalsReviewer": protocol.AutoReviewer}
	if state.CWD != "" {
		resume["cwd"] = state.CWD
	}
	requests := []map[string]any{
		{"id": "jara-bootstrap-initialize", "method": "initialize", "params": chatInitializeParams()},
		{"method": "initialized"},
		{"id": "jara-bootstrap-resume", "method": "thread/resume", "params": resume},
	}
	pending[`"jara-bootstrap-initialize"`] = "initialize"
	pending[`"jara-bootstrap-resume"`] = "thread/resume"
	for _, request := range requests {
		if line, err := json.Marshal(request); err == nil {
			_, _ = input.Write(append(line, '\n'))
		}
	}
	state.Initialized = true
}
