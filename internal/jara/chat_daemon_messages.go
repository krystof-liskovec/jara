package jara

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

type chatDaemonClientClosed struct {
	conn   net.Conn
	close  bool
	detach bool
}

func writeChatDaemonState(paths chatDaemonPaths, state chatDaemonState) error {
	return writeJSON(paths.state, state)
}

func removeChatDaemonPID(paths chatDaemonPaths, pid int) {
	data, err := os.ReadFile(paths.pid)
	if err == nil && strings.TrimSpace(string(data)) == strconv.Itoa(pid) {
		_ = os.Remove(paths.pid)
	}
}

func readChatDaemonClient(conn net.Conn, input io.WriteCloser, inputMu *sync.Mutex, state *chatDaemonState, stateMu, stateWriteMu *sync.Mutex, paths chatDaemonPaths, pending map[string]string, pendingMu *sync.Mutex, serverRequests *chatDaemonRequestQueue, closed chan<- chatDaemonClientClosed) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	requestedClose := false
	requestedDetach := false
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var message appMessage
		if json.Unmarshal(line, &message) == nil {
			if message.Method == "jara/close" || message.Method == "jara/detach" {
				requestedClose = message.Method == "jara/close"
				requestedDetach = message.Method == "jara/detach"
				break
			}
			if len(message.ID) > 0 && message.Method != "" {
				pendingMu.Lock()
				pending[string(message.ID)] = message.Method
				pendingMu.Unlock()
			} else if len(message.ID) > 0 {
				serverRequests.remove(message.ID)
			}
			if message.Method == "turn/start" {
				stateWriteMu.Lock()
				stateMu.Lock()
				state.ActiveTurn = true
				snapshot := *state
				stateMu.Unlock()
				_ = writeChatDaemonState(paths, snapshot)
				stateWriteMu.Unlock()
			}
		}
		inputMu.Lock()
		_, err := input.Write(append(line, '\n'))
		inputMu.Unlock()
		if err != nil {
			break
		}
	}
	closed <- chatDaemonClientClosed{conn: conn, close: requestedClose, detach: requestedDetach}
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
		if (method == "thread/start" || method == "thread/resume") && message.Error == nil {
			var response struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if json.Unmarshal(message.Result, &response) == nil && response.Thread.ID != "" {
				stateMu.Lock()
				state.Conversation = response.Thread.ID
				stateMu.Unlock()
				changed = true
			}
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

func writeChatDaemonError(conn net.Conn, message string) error {
	return json.NewEncoder(conn).Encode(map[string]any{"method": "jara/error", "params": map[string]string{"message": message}})
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

func (q *chatDaemonRequestQueue) remove(id json.RawMessage) {
	q.mu.Lock()
	if _, ok := q.byID[string(id)]; ok {
		delete(q.byID, string(id))
	}
	q.mu.Unlock()
}

func (q *chatDaemonRequestQueue) writePending(conn net.Conn) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	seen := map[string]bool{}
	for _, id := range q.order {
		if seen[id] {
			continue
		}
		seen[id] = true
		if line, ok := q.byID[id]; ok {
			if err := writeChatDaemonLine(conn, line); err != nil {
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
