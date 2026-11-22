package jara

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

type chatDaemonClientClosed struct {
	client       *chatDaemonClient
	close        bool
	detach       bool
	stop         bool
	handoff      bool
	suspend      bool
	release      bool // a local runner is asked to hand the workspace over
	disconnected bool
}

func writeChatDaemonState(paths chatDaemonPaths, state chatDaemonState) error {
	return fsutil.WriteJSON(paths.state, state)
}

func removeChatDaemonPID(paths chatDaemonPaths, pid int) {
	data, err := os.ReadFile(paths.pid)
	if err == nil && strings.TrimSpace(string(data)) == strconv.Itoa(pid) {
		_ = os.Remove(paths.pid)
	}
}

func readChatDaemonClient(client *chatDaemonClient, input io.WriteCloser, inputMu *sync.Mutex, state *chatDaemonState, stateMu, stateWriteMu *sync.Mutex, paths chatDaemonPaths, pending map[string]string, pendingMu *sync.Mutex, router *chatDaemonClientRouter, serverRequests *chatDaemonRequestQueue, schedules *chatScheduleManager, crunch *chatCrunchManager, closed chan<- chatDaemonClientClosed) {
	scanner := protocol.NewLineScanner(client.conn)
	requestedClose := false
	requestedDetach := false
	requestedStop := false
	requestedHandoff := false
	requestedSuspend := false
	requestedRelease := false
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var message appMessage
		if json.Unmarshal(line, &message) == nil {
			if message.Method == "jara/close" || message.Method == "jara/detach" || message.Method == "jara/stop" || message.Method == "jara/handoff" || message.Method == "jara/suspend" || message.Method == "jara/release" {
				requestedClose = message.Method == "jara/close" || message.Method == "jara/stop"
				requestedDetach = message.Method == "jara/detach"
				requestedStop = message.Method == "jara/stop"
				requestedHandoff = message.Method == "jara/handoff"
				requestedSuspend = message.Method == "jara/suspend"
				requestedRelease = message.Method == "jara/release"
				break
			}
			if message.Method == "initialized" {
				stateWriteMu.Lock()
				stateMu.Lock()
				state.Initialized = true
				snapshot := *state
				stateMu.Unlock()
				_ = writeChatDaemonState(paths, snapshot)
				stateWriteMu.Unlock()
			}
			if message.Method == "jara/git/diff" {
				if len(message.ID) > 0 {
					stateMu.Lock()
					cwd := state.CWD
					stateMu.Unlock()
					handleChatGitDiffRequest(message.ID, cwd, client)
				}
				continue
			}
			if message.Method == chatProjectLocationMethod {
				if len(message.ID) > 0 {
					stateMu.Lock()
					cwd := state.CWD
					stateMu.Unlock()
					handleChatProjectLocationRequest(message.ID, cwd, client)
				}
				continue
			}
			if message.Method == "jara/shell" {
				if len(message.ID) > 0 {
					stateMu.Lock()
					cwd := state.CWD
					stateMu.Unlock()
					result := runChatShellRequest(message.Params, cwd)
					response, marshalErr := json.Marshal(map[string]any{"id": message.ID, "result": result})
					if marshalErr == nil {
						_ = client.write(response)
					}
				}
				continue
			}
			if chatNotifyRequest(message.Method) {
				if len(message.ID) > 0 {
					stateMu.Lock()
					workspace := state.Workspace
					stateMu.Unlock()
					result, requestErr := handleChatNotifyRequest(message.Method, message.Params, workspace)
					response := map[string]any{"id": message.ID}
					if requestErr != nil {
						response["error"] = map[string]any{"code": -32602, "message": requestErr.Error()}
					} else {
						response["result"] = result
					}
					if line, marshalErr := json.Marshal(response); marshalErr == nil {
						_ = client.write(line)
					}
				}
				continue
			}
			if chatIdleCompactRequest(message.Method) {
				if len(message.ID) > 0 {
					stateMu.Lock()
					agent := state.Agent
					stateMu.Unlock()
					result, requestErr := handleChatIdleCompactRequest(message.Method, message.Params, agent)
					response := map[string]any{"id": message.ID}
					if requestErr != nil {
						response["error"] = map[string]any{"code": -32602, "message": requestErr.Error()}
					} else {
						response["result"] = result
					}
					if line, marshalErr := json.Marshal(response); marshalErr == nil {
						_ = client.write(line)
					}
				}
				continue
			}
			if chatScheduleRequest(message.Method) || chatQueueRequest(message.Method) {
				if len(message.ID) > 0 {
					stateMu.Lock()
					threadID := state.requestThread(message.Params)
					stateMu.Unlock()
					result, requestErr := schedules.handleClientRequest(message.Method, message.Params, threadID)
					response := map[string]any{"id": message.ID}
					if requestErr != nil {
						response["error"] = map[string]any{"code": -32602, "message": requestErr.Error()}
					} else {
						response["result"] = result
					}
					if line, marshalErr := json.Marshal(response); marshalErr == nil {
						_ = client.write(line)
					}
				}
				continue
			}
			if chatCrunchRequest(message.Method) {
				if len(message.ID) > 0 {
					stateMu.Lock()
					threadID := state.requestThread(message.Params)
					stateMu.Unlock()
					result, requestErr := crunch.handleClientRequest(message.Method, message.Params, threadID, input, inputMu)
					response := map[string]any{"id": message.ID}
					if requestErr != nil {
						response["error"] = map[string]any{"code": -32602, "message": requestErr.Error()}
					} else {
						response["result"] = result
					}
					if line, marshalErr := json.Marshal(response); marshalErr == nil {
						_ = client.write(line)
					}
				}
				continue
			}
			if len(message.ID) > 0 && message.Method == "" {
				if serverRequests.claim(message.ID) {
					inputMu.Lock()
					_, _ = input.Write(append(line, '\n'))
					inputMu.Unlock()
				}
				continue
			}
		}
		forwardLine := line
		virtualID := message.ID
		// A message to a subagent starts a turn in its own thread; it neither
		// waits for nor marks the conversation's turn.
		turnThread, subagentTurn := chatDaemonTurnTarget(message.Params)
		subagentTurn = subagentTurn && message.Method == "turn/start"
		if len(message.ID) > 0 && message.Method != "" {
			var rewriteErr error
			forwardLine, virtualID, rewriteErr = router.rewrite(client, line, message.ID)
			if rewriteErr != nil {
				continue
			}
			method := message.Method
			if subagentTurn {
				method = chatDaemonSubagentTurn
			}
			pendingMu.Lock()
			pending[string(virtualID)] = method
			pendingMu.Unlock()
		}
		inputMu.Lock()
		if message.Method == "turn/start" && !subagentTurn {
			stateMu.Lock()
			activeTurn := state.ActiveTurn
			stateMu.Unlock()
			if activeTurn || schedules.running() || crunch.running() {
				inputMu.Unlock()
				pendingMu.Lock()
				delete(pending, string(virtualID))
				pendingMu.Unlock()
				router.discard(virtualID)
				response, _ := json.Marshal(map[string]any{
					"id": message.ID,
					"error": map[string]any{
						"code":    -32000,
						"message": "another turn is already running; wait for it to finish before sending another prompt",
					},
				})
				_ = client.write(response)
				continue
			}
			stateWriteMu.Lock()
			stateMu.Lock()
			state.startConversationTurn(turnThread)
			if model := chatDaemonRequestModel(message.Params); model != "" {
				state.Agent = protocol.AgentForModel(model) // a model switch can change the agent
			}
			snapshot := *state
			stateMu.Unlock()
			_ = writeChatDaemonState(paths, snapshot)
			stateWriteMu.Unlock()
		} else if message.Method == "thread/start" || message.Method == "thread/resume" {
			stateWriteMu.Lock()
			stateMu.Lock()
			state.ThreadLoaded = false
			snapshot := *state
			stateMu.Unlock()
			_ = writeChatDaemonState(paths, snapshot)
			stateWriteMu.Unlock()
		}
		_, err := input.Write(append(forwardLine, '\n'))
		inputMu.Unlock()
		if err != nil {
			if len(virtualID) > 0 && message.Method != "" {
				pendingMu.Lock()
				delete(pending, string(virtualID))
				pendingMu.Unlock()
				router.discard(virtualID)
			}
			break
		}
	}
	closed <- chatDaemonClientClosed{
		client: client, close: requestedClose, detach: requestedDetach,
		stop: requestedStop, handoff: requestedHandoff, suspend: requestedSuspend, release: requestedRelease,
		disconnected: !requestedClose && !requestedDetach && !requestedStop && !requestedHandoff && !requestedSuspend && !requestedRelease,
	}
}

func applyChatDaemonEvent(line []byte, state *chatDaemonState, stateMu *sync.Mutex, pending *map[string]string, pendingMu *sync.Mutex) bool {
	var message appMessage
	if json.Unmarshal(line, &message) != nil {
		return false
	}
	changed := false
	stateMu.Lock()
	if message.Method == "turn/started" || message.Method == "turn/completed" {
		var target struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(message.Params, &target)
		if !state.conversationTurnEvent(target.ThreadID) {
			stateMu.Unlock()
			return false // a subagent's turn, not the conversation's
		}
		if message.Method == "turn/started" && target.ThreadID != "" {
			state.TurnThread = target.ThreadID
		}
	}
	switch message.Method {
	case "turn/started":
		state.ActiveTurn = true
		changed = true
	case "turn/completed":
		state.ActiveTurn, state.TurnThread = false, ""
		changed = true
	case protocol.TurnSelectionUpdated:
		var update protocol.TurnSelectionUpdate
		if json.Unmarshal(message.Params, &update) == nil && update.ThreadID == state.Conversation &&
			update.Applied != nil && update.Applied.Model != "" {
			state.Agent = protocol.AgentForModel(update.Applied.Model)
			changed = true
		}
	}
	stateMu.Unlock()
	if len(message.ID) > 0 && message.Method == "" {
		pendingMu.Lock()
		method := (*pending)[string(message.ID)]
		delete(*pending, string(message.ID))
		pendingMu.Unlock()
		if method == "initialize" && message.Error != nil && chatAlreadyInitialized(message.Error.Message) {
			stateMu.Lock()
			state.Initialized = true
			stateMu.Unlock()
			changed = true
		}
		if (method == "thread/start" || method == "thread/resume") && message.Error == nil {
			var response struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
				Model string `json:"model"`
			}
			if json.Unmarshal(message.Result, &response) == nil && response.Thread.ID != "" {
				stateMu.Lock()
				state.Conversation = response.Thread.ID
				state.ThreadLoaded = true
				state.noteOpened(response.Thread.ID)
				if response.Model != "" {
					state.Agent = protocol.AgentForModel(response.Model)
				}
				stateMu.Unlock()
				changed = true
			}
		} else if (method == "thread/start" || method == "thread/resume") && message.Error != nil {
			stateMu.Lock()
			state.ThreadLoaded = state.Conversation != ""
			stateMu.Unlock()
			changed = true
		}
		// An idle compaction marks the turn active before its turn starts.
		if (method == "turn/start" || method == "thread/compact/start") && message.Error != nil {
			stateMu.Lock()
			state.ActiveTurn, state.TurnThread = false, ""
			stateMu.Unlock()
			changed = true
		}
	}
	return changed
}

type chatDaemonRequestQueue struct {
	mu    sync.Mutex
	byID  map[string][]byte
	order []string
}

func newChatDaemonRequestQueue() *chatDaemonRequestQueue {
	return &chatDaemonRequestQueue{byID: map[string][]byte{}}
}

func (q *chatDaemonRequestQueue) add(id json.RawMessage, line []byte) {
	key := string(id)
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.byID[key]; !ok {
		q.order = append(q.order, key)
	}
	q.byID[key] = append([]byte(nil), line...)
}

// claim takes a request a client answered; only the first answer counts.
func (q *chatDaemonRequestQueue) claim(id json.RawMessage) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.forgetLocked(string(id))
}

// remove drops a request the app-server settled itself, so it is not
// replayed to clients that attach later.
func (q *chatDaemonRequestQueue) remove(id json.RawMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.forgetLocked(string(id))
}

func (q *chatDaemonRequestQueue) forgetLocked(key string) bool {
	if _, ok := q.byID[key]; !ok {
		return false
	}
	delete(q.byID, key)
	q.order = slices.DeleteFunc(q.order, func(queued string) bool { return queued == key })
	return true
}

func (q *chatDaemonRequestQueue) writePending(client *chatDaemonClient) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	seen := map[string]bool{}
	for _, id := range q.order {
		if seen[id] {
			continue
		}
		seen[id] = true
		if line, ok := q.byID[id]; ok {
			if err := client.write(line); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeChatDaemonLine(conn net.Conn, line []byte) error {
	line = append(line, '\n')
	for len(line) > 0 {
		written, err := conn.Write(line)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		line = line[written:]
	}
	return nil
}

// chatDaemonSubagentTurn records a turn/start sent to a subagent's thread.
const chatDaemonSubagentTurn = "turn/start#subagent"

// chatDaemonRequestModel is the model a turn/start request selects, or "".
func chatDaemonRequestModel(params json.RawMessage) string {
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(params, &request)
	return request.Model
}
