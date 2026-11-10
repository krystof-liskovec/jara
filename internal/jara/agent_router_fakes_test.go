package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as fake agent CLIs: with JARA_FAKE_AGENT=1 it acts
// as `claude` (stream-json) or `codex app-server`, logging what it received.
func TestMain(m *testing.M) {
	if os.Getenv("JARA_FAKE_AGENT") == "1" {
		if slices.Contains(os.Args, "--input-format") {
			fakeClaude()
		} else {
			fakeCodex()
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeLog(kind, text string) {
	file, err := os.OpenFile(os.Getenv("JARA_FAKE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	data, _ := json.Marshal(map[string]string{"kind": kind, "text": text})
	fmt.Fprintf(file, "%s\n", data)
}

func fakeClaude() {
	fakeLog("claude-args", strings.Join(os.Args[1:], " "))
	out := json.NewEncoder(os.Stdout)
	emit := func(v any) { _ = out.Encode(v) }
	text := func(s string) {
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "text"}}})
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": s}}})
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_stop"}})
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	next := func() map[string]any {
		if !scanner.Scan() {
			os.Exit(0)
		}
		var message map[string]any
		_ = json.Unmarshal(scanner.Bytes(), &message)
		return message
	}
	continuations := 0
	for {
		message := next()
		switch message["type"] {
		case "control_request":
			request := message["request"].(map[string]any)
			fakeLog("claude-control", fmt.Sprint(request["subtype"], " ", request["model"], request["mode"]))
			emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": message["request_id"]}})
			continue
		case "user":
		default:
			continue
		}
		prompt := message["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		fakeLog("claude-prompt", prompt)
		switch {
		case strings.Contains(prompt, "EDIT"):
			input := map[string]any{"file_path": "/w/app.txt", "old_string": "beta", "new_string": "BETA"}
			emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Edit", "input": input}}}})
			emit(map[string]any{"type": "control_request", "request_id": "perm-1", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Edit", "input": input, "tool_use_id": "toolu_1", "description": "app.txt"}})
			var answer map[string]any
			for answer == nil || answer["type"] != "control_response" {
				answer = next()
			}
			behavior := answer["response"].(map[string]any)["response"].(map[string]any)["behavior"]
			fakeLog("claude-permission", fmt.Sprint(behavior))
			result := map[string]any{"filePath": "/w/app.txt", "structuredPatch": []any{map[string]any{"oldStart": 1, "oldLines": 2, "newStart": 1, "newLines": 2, "lines": []string{" alpha", "-beta", "+BETA"}}}}
			emit(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "updated"}}}, "tool_use_result": result})
			text("edited")
			emit(map[string]any{"type": "result", "subtype": "success"})
		case strings.Contains(prompt, "LIMIT"):
			emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected", "resetsAt": time.Now().Add(2 * time.Hour).Unix(), "rateLimitType": "five_hour",
				"unifiedWindows": map[string]any{"five_hour": map[string]any{"utilization": 1.0, "status": "rejected", "resetsAt": time.Now().Add(2 * time.Hour).Unix()}}}})
			emit(map[string]any{"type": "result", "subtype": "success", "is_error": true, "result": "You've hit your limit · resets 5pm"})
		case strings.Contains(prompt, "RESET"):
			emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed", "rateLimitType": "five_hour",
				"unifiedWindows": map[string]any{"five_hour": map[string]any{"utilization": 0.1, "status": "allowed", "resetsAt": time.Now().Add(5 * time.Hour).Unix()}}}})
			text("back")
			emit(map[string]any{"type": "result", "subtype": "success"})
		case strings.Contains(prompt, "SLOW"):
			text("working")
			for {
				m := next()
				if m["type"] == "control_request" && m["request"].(map[string]any)["subtype"] == "interrupt" {
					fakeLog("claude-control", "interrupt")
					emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true})
					break
				}
			}
		case strings.Contains(prompt, "Continue working toward the goal."):
			continuations++
			reply := "step " + fmt.Sprint(continuations)
			if continuations >= 2 {
				reply += "\nGOAL COMPLETE: all done"
			}
			emit(map[string]any{"type": "system", "subtype": "thinking_tokens", "estimated_tokens": 120})
			text(reply)
			emit(map[string]any{"type": "result", "subtype": "success", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}})
		default:
			text("claude: " + lastLine(prompt))
			emit(map[string]any{"type": "result", "subtype": "success"})
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}

func fakeCodex() {
	out := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	threads := 0
	for scanner.Scan() {
		var m rpcMessage
		_ = json.Unmarshal(scanner.Bytes(), &m)
		if len(m.ID) == 0 || m.Method == "" {
			continue
		}
		reply := func(result any) { _ = out.Encode(map[string]any{"id": m.ID, "result": result}) }
		var params map[string]any
		_ = json.Unmarshal(m.Params, &params)
		switch m.Method {
		case "initialize":
			reply(map[string]any{})
		case "model/list":
			reply(map[string]any{"data": []any{map[string]any{"id": "gpt-x", "model": "gpt-x", "displayName": "GPT X"}}, "nextCursor": nil})
		case "thread/start":
			threads++
			reply(map[string]any{"thread": map[string]any{"id": fmt.Sprintf("codex-thread-%d", threads), "cwd": params["cwd"], "turns": []any{}}, "model": "gpt-x"})
		case "thread/resume":
			reply(map[string]any{"thread": map[string]any{"id": params["threadId"], "turns": []any{}}, "model": "gpt-x"})
		case "account/rateLimits/read":
			fakeLog("codex-ratelimits", string(m.Params))
			reply(map[string]any{"rateLimits": map[string]any{"primary": map[string]any{"usedPercent": 5, "windowDurationMins": 300, "resetsAt": 1}}})
		case "thread/goal/set":
			_, marker := params["jaraDriver"]
			fakeLog("codex-goal", fmt.Sprint(params["threadId"], "|", params["objective"], "|", params["status"], "|marker=", marker))
			reply(map[string]any{"goal": params})
		case "thread/list":
			reply(map[string]any{"data": []any{map[string]any{"id": "codex-thread-1", "cwd": "/w"}, map[string]any{"id": "codex-thread-2", "cwd": "/w"}, map[string]any{"id": "codex-other", "cwd": "/w"}}, "nextCursor": nil})
		case "turn/start":
			text := params["input"].([]any)[0].(map[string]any)["text"].(string)
			fakeLog("codex-turn", fmt.Sprint(params["threadId"], "|", text))
			thread := params["threadId"]
			reply(map[string]any{"turn": map[string]any{"id": "ct"}})
			for _, n := range []map[string]any{
				{"method": "turn/started", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": "ct"}}},
				{"method": "item/completed", "params": map[string]any{"threadId": thread, "item": map[string]any{"id": "m", "type": "agentMessage", "text": "codex: " + lastLine(text)}}},
				{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": "ct", "status": "completed"}}},
			} {
				_ = out.Encode(n)
			}
		default:
			_ = out.Encode(map[string]any{"id": m.ID, "error": map[string]any{"message": "unsupported " + m.Method}})
		}
	}
}

type routerHarness struct {
	t      *testing.T
	in     *os.File
	lines  chan map[string]any
	nextID int
}

func startRouterHarness(t *testing.T, defaultAgent string, claudeArgs ...string) *routerHarness {
	t.Helper()
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	go func() {
		_ = runAgentRouter(inR, outW, []string{os.Args[0], "app-server"}, defaultAgent, claudeArgs...)
		outW.Close()
	}()
	h := &routerHarness{t: t, in: inW, lines: make(chan map[string]any, 256)}
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

func (h *routerHarness) request(method string, params any) float64 {
	h.nextID++
	data, _ := json.Marshal(map[string]any{"id": h.nextID, "method": method, "params": params})
	_, _ = h.in.Write(append(data, '\n'))
	return float64(h.nextID)
}

func (h *routerHarness) send(message any) {
	data, _ := json.Marshal(message)
	_, _ = h.in.Write(append(data, '\n'))
}

// waitFor returns the first message matching want, collecting everything seen.
func (h *routerHarness) waitFor(want func(map[string]any) bool) (map[string]any, []map[string]any) {
	h.t.Helper()
	var seen []map[string]any
	timeout := time.After(10 * time.Second)
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

func (h *routerHarness) response(id float64) map[string]any {
	m, _ := h.waitFor(func(m map[string]any) bool { v, ok := m["id"].(float64); return ok && v == id && m["method"] == nil })
	return m
}

func isMethod(method string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["method"] == method }
}

func readFakeLog(t *testing.T, kind string) []string {
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
