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
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var message appMessage
		if json.Unmarshal(line, &message) == nil {
			if message.Method == "jara/close" || message.Method == "jara/detach" || message.Method == "jara/stop" || message.Method == "jara/handoff" {
				requestedClose = message.Method == "jara/close" || message.Method == "jara/stop"
				requestedDetach = message.Method == "jara/detach"
				requestedStop = message.Method == "jara/stop"
				requestedHandoff = message.Method == "jara/handoff"
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
			if chatScheduleRequest(message.Method) {
				if len(message.ID) > 0 {
					stateMu.Lock()
					threadID := state.Conversation
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
					threadID := state.Conversation
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
		if len(message.ID) > 0 && message.Method != "" {
			var rewriteErr error
			forwardLine, virtualID, rewriteErr = router.rewrite(client, line, message.ID)
			if rewriteErr != nil {
				continue
			}
			pendingMu.Lock()
			pending[string(virtualID)] = message.Method
			pendingMu.Unlock()
		}
		inputMu.Lock()
		if message.Method == "turn/start" {
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
			state.ActiveTurn = true
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
		stop: requestedStop, handoff: requestedHandoff,
		disconnected: !requestedClose && !requestedDetach && !requestedStop && !requestedHandoff,
	}
}

func applyChatDaemonEvent(line []byte, state *chatDaemonState, stateMu *sync.Mutex, pending *map[string]string, pendingMu *sync.Mutex) bool {
	var message appMessage
	if json.Unmarshal(line, &message) != nil {
		return false
	}
	changed := false
	stateMu.Lock()
	switch message.Method {
	case "turn/started":
		state.ActiveTurn = true
		changed = true
	case "turn/completed":
		state.ActiveTurn = false
		changed = true
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
			}
			if json.Unmarshal(message.Result, &response) == nil && response.Thread.ID != "" {
				stateMu.Lock()
				state.Conversation = response.Thread.ID
				state.ThreadLoaded = true
				stateMu.Unlock()
				changed = true
			}
		} else if (method == "thread/start" || method == "thread/resume") && message.Error != nil {
			stateMu.Lock()
			state.ThreadLoaded = state.Conversation != ""
			stateMu.Unlock()
			changed = true
		}
		if method == "turn/start" && message.Error != nil {
			stateMu.Lock()
			state.ActiveTurn = false
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
