package jara

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

type chatDaemonClient struct {
	conn       net.Conn
	writeMu    sync.Mutex
	reconnects bool // a relay for a client that reconnects after the session stops
}

func (c *chatDaemonClient) write(line []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return writeChatDaemonLine(c.conn, line)
}

// chatClientReconnects reports whether the peer is a relay for a client that
// reconnects by itself; tests replace it.
var chatClientReconnects = func(conn net.Conn) bool { return reconnectingChatRelay(chatPeerPID(conn)) }

type chatDaemonClientHub struct {
	mu       sync.Mutex
	clients  map[*chatDaemonClient]struct{}
	requests *chatDaemonRequestQueue
}

func newChatDaemonClientHub(requests *chatDaemonRequestQueue) *chatDaemonClientHub {
	return &chatDaemonClientHub{clients: map[*chatDaemonClient]struct{}{}, requests: requests}
}

func (h *chatDaemonClientHub) attach(conn net.Conn, ready remoteChatServerReady) (*chatDaemonClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	client := &chatDaemonClient{conn: conn, reconnects: chatClientReconnects(conn)}
	if err := writeChatDaemonReady(conn, ready); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := h.requests.writePending(client); err != nil {
		_ = conn.Close()
		return nil, err
	}
	h.clients[client] = struct{}{}
	return client, nil
}

func (h *chatDaemonClientHub) detach(client *chatDaemonClient) {
	h.mu.Lock()
	delete(h.clients, client)
	h.mu.Unlock()
	_ = client.conn.Close()
}

func (h *chatDaemonClientHub) closeAll() {
	h.mu.Lock()
	clients := make([]*chatDaemonClient, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
		delete(h.clients, client)
	}
	h.mu.Unlock()
	for _, client := range clients {
		_ = client.conn.Close()
	}
}

func (h *chatDaemonClientHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *chatDaemonClientHub) reconnecting() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for client := range h.clients {
		if client.reconnects {
			count++
		}
	}
	return count
}

func (h *chatDaemonClientHub) broadcast(line []byte, message appMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if message.Method != "" && len(message.ID) > 0 {
		h.requests.add(message.ID, line)
	}
	if message.Method == "serverRequest/resolved" {
		var resolved struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(message.Params, &resolved) == nil && len(resolved.RequestID) > 0 {
			h.requests.remove(resolved.RequestID)
		}
	}
	for client := range h.clients {
		if err := client.write(line); err != nil {
			delete(h.clients, client)
			_ = client.conn.Close()
		}
	}
}

type chatDaemonClientRoute struct {
	client     *chatDaemonClient
	originalID json.RawMessage
}

type chatDaemonClientRouter struct {
	mu     sync.Mutex
	next   uint64
	routes map[string]chatDaemonClientRoute
}

func newChatDaemonClientRouter() *chatDaemonClientRouter {
	return &chatDaemonClientRouter{routes: map[string]chatDaemonClientRoute{}}
}

func (r *chatDaemonClientRouter) rewrite(client *chatDaemonClient, line []byte, originalID json.RawMessage) ([]byte, json.RawMessage, error) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return nil, nil, err
	}
	r.mu.Lock()
	r.next++
	virtualID, _ := json.Marshal(fmt.Sprintf("jara-client-%d", r.next))
	r.routes[string(virtualID)] = chatDaemonClientRoute{client: client, originalID: append(json.RawMessage(nil), originalID...)}
	r.mu.Unlock()
	message["id"] = virtualID
	rewritten, err := json.Marshal(message)
	if err != nil {
		r.discard(virtualID)
		return nil, nil, err
	}
	return rewritten, virtualID, nil
}

func (r *chatDaemonClientRouter) discard(id json.RawMessage) {
	r.mu.Lock()
	delete(r.routes, string(id))
	r.mu.Unlock()
}

func (r *chatDaemonClientRouter) response(line []byte, id json.RawMessage) (*chatDaemonClient, []byte, bool) {
	r.mu.Lock()
	route, ok := r.routes[string(id)]
	if ok {
		delete(r.routes, string(id))
	}
	r.mu.Unlock()
	if !ok {
		return nil, nil, false
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(line, &message) != nil {
		return route.client, nil, true
	}
	message["id"] = route.originalID
	rewritten, err := json.Marshal(message)
	if err != nil {
		return route.client, nil, true
	}
	return route.client, rewritten, true
}
