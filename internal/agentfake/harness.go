package agentfake

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Setup points Jara at the fake agents: the test binary plays Claude Code and
// Codex, and data and logs go to a temporary directory.
func Setup(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JARA_FAKE_AGENT", "1")
	t.Setenv("JARA_FAKE_LOG", filepath.Join(dir, "log.jsonl"))
	t.Setenv("JARA_CLAUDE_BIN", "/proc/self/exe")
	t.Setenv("JARA_DATA", filepath.Join(dir, "data"))
	t.Setenv("JARA_CLAUDE_ARGS", "[]")
}

// CodexCommand starts the fake Codex app-server.
func CodexCommand() []string { return []string{os.Args[0], "app-server"} }

// Harness talks JSON-RPC to an agent router running in the test process.
type Harness struct {
	t      *testing.T
	in     *os.File
	lines  chan map[string]any
	nextID int
	Wait   time.Duration // how long WaitFor waits; real agents need longer
}

// StartHarness runs the router with run, which serves input and output the
// way the router serves the Jara interface.
func StartHarness(t *testing.T, run func(input io.Reader, output io.Writer) error) *Harness {
	t.Helper()
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	go func() {
		_ = run(inR, outW)
		outW.Close()
	}()
	h := &Harness{t: t, in: inW, lines: make(chan map[string]any, 256)}
	go func() {
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 64*1024), 8<<20)
		for scanner.Scan() {
			var m map[string]any
			if json.Unmarshal(scanner.Bytes(), &m) == nil {
				h.lines <- m
			}
		}
		close(h.lines)
	}()
	t.Cleanup(func() { inW.Close() })
	return h
}

// Request sends a request and returns its ID.
func (h *Harness) Request(method string, params any) float64 {
	h.nextID++
	data, _ := json.Marshal(map[string]any{"id": h.nextID, "method": method, "params": params})
	_, _ = h.in.Write(append(data, '\n'))
	return float64(h.nextID)
}

// Send writes any message, such as a reply to a server request.
func (h *Harness) Send(message any) {
	data, _ := json.Marshal(message)
	_, _ = h.in.Write(append(data, '\n'))
}

// WaitFor returns the first message matching want, collecting everything seen.
func (h *Harness) WaitFor(want func(map[string]any) bool) (map[string]any, []map[string]any) {
	h.t.Helper()
	var seen []map[string]any
	timeout := time.After(max(h.Wait, 10*time.Second))
	for {
		select {
		case m, ok := <-h.lines:
			if !ok {
				h.t.Fatalf("router closed; saw %v", seen)
			}
			seen = append(seen, m)
			if want(m) {
				return m, seen
			}
		case <-timeout:
			h.t.Fatalf("timed out; saw %v", seen)
		}
	}
}

// Response waits for the response to request id.
func (h *Harness) Response(id float64) map[string]any {
	m, _ := h.WaitFor(func(m map[string]any) bool { v, ok := m["id"].(float64); return ok && v == id && m["method"] == nil })
	return m
}

// Raw sends a request with a string ID and waits for its response.
func (h *Harness) Raw(id string, method string, params any) map[string]any {
	h.Send(map[string]any{"id": id, "method": method, "params": params})
	m, _ := h.WaitFor(func(m map[string]any) bool { return m["id"] == id && m["method"] == nil })
	return m
}

// IsMethod matches notifications and requests by method.
func IsMethod(method string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["method"] == method }
}

// ReadLog returns what the fake agents logged under kind.
func ReadLog(t *testing.T, kind string) []string {
	t.Helper()
	data, _ := os.ReadFile(os.Getenv("JARA_FAKE_LOG"))
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]string
		if json.Unmarshal([]byte(line), &entry) == nil && entry["kind"] == kind {
			out = append(out, entry["text"])
		}
	}
	return out
}

// AgentMessages joins the streamed and completed agent messages seen.
func AgentMessages(seen []map[string]any) string {
	var b strings.Builder
	for _, m := range seen {
		params, _ := m["params"].(map[string]any)
		if m["method"] == "item/agentMessage/delta" {
			b.WriteString(params["delta"].(string))
		}
		if m["method"] == "item/completed" {
			if item, _ := params["item"].(map[string]any); item["type"] == "agentMessage" {
				b.WriteString("|" + item["text"].(string))
			}
		}
	}
	return b.String()
}

// ThreadIDs collects the thread IDs that messages referred to.
func ThreadIDs(seen []map[string]any) map[string]bool {
	ids := map[string]bool{}
	for _, m := range seen {
		if params, ok := m["params"].(map[string]any); ok {
			if id, ok := params["threadId"].(string); ok {
				ids[id] = true
			}
		}
	}
	return ids
}

// HasResponse reports whether a successful response to id was seen.
func HasResponse(seen []map[string]any, id float64) bool {
	for _, m := range seen {
		if v, ok := m["id"].(float64); ok && v == id && m["method"] == nil {
			return m["error"] == nil
		}
	}
	return false
}

// WaitForLog waits until a fake agent logged an entry of kind containing
// text. A request the router merely forwarded may not have reached the fake
// yet, and a process restarted meanwhile would never log it.
func WaitForLog(t *testing.T, kind, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, entry := range ReadLog(t, kind) {
			if strings.Contains(entry, text) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake agent never logged %s %q; it logged %q", kind, text, ReadLog(t, kind))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
