// Package agentfake turns a test binary into fake agent CLIs: with
// JARA_FAKE_AGENT=1 it acts as `claude` (stream-json, or a one-shot side
// question) or `codex app-server`, logging what it received to JARA_FAKE_LOG.
// Test suites call ServeIfRequested from TestMain.
package agentfake

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// ServeIfRequested runs the fake agent the arguments ask for and exits when
// JARA_FAKE_AGENT=1; otherwise it returns.
func ServeIfRequested() {
	if os.Getenv("JARA_FAKE_AGENT") != "1" {
		return
	}
	if slices.Contains(os.Args, "--no-session-persistence") {
		question, _ := io.ReadAll(os.Stdin)
		Log("claude-btw", strings.Join(os.Args[1:], " ")+"|"+string(question))
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "result": "claude btw: " + string(question)})
	} else if slices.Contains(os.Args, "--input-format") {
		fakeClaude()
	} else {
		fakeCodex()
	}
	os.Exit(0)
}

func Log(kind, text string) {
	file, err := os.OpenFile(os.Getenv("JARA_FAKE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	data, _ := json.Marshal(map[string]string{"kind": kind, "text": text})
	fmt.Fprintf(file, "%s\n", data)
}

func fakeClaude() {
	Log("claude-args", strings.Join(os.Args[1:], " "))
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
			Log("claude-control", fmt.Sprint(request["subtype"], " ", request["model"], request["mode"]))
			emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": message["request_id"]}})
			continue
		case "user":
		default:
			continue
		}
		content := message["message"].(map[string]any)["content"].([]any)
		prompt := content[0].(map[string]any)["text"].(string)
		Log("claude-prompt", prompt)
		if len(content) > 1 {
			Log("claude-image", fmt.Sprint(content[1].(map[string]any)["source"].(map[string]any)["media_type"]))
		}
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
			Log("claude-permission", fmt.Sprint(behavior))
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
					Log("claude-control", "interrupt")
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
			emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{}, "usage": map[string]any{"input_tokens": 100, "cache_read_input_tokens": 900, "output_tokens": 10}}})
			text("claude: " + LastLine(prompt))
			emit(map[string]any{"type": "result", "subtype": "success", "modelUsage": map[string]any{"claude-x": map[string]any{"contextWindow": 400000}}})
		}
	}
}

func LastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}

func fakeCodex() {
	out := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	threads := 0
	for scanner.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
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
		case "thread/fork":
			Log("codex-fork", fmt.Sprint(params["threadId"], "|", params["ephemeral"], "|", params["sandbox"]))
			fork := map[string]any{"id": "codex-fork", "ephemeral": true, "turns": []any{}}
			_ = out.Encode(map[string]any{"method": "thread/started", "params": map[string]any{"thread": fork}})
			reply(map[string]any{"thread": fork, "model": "gpt-x"})
		case "config/read":
			reply(map[string]any{"config": map[string]any{"model": os.Getenv("JARA_FAKE_CODEX_MODEL")}})
		case "config/value/write":
			Log("codex-config", fmt.Sprint(params["keyPath"], "=", params["value"]))
			reply(map[string]any{})
		case "thread/revert":
			Log("codex-revert", fmt.Sprint(params["threadId"], "|", params["beforeTurnId"]))
			reply(map[string]any{"thread": map[string]any{"id": params["threadId"]}})
		case "thread/unsubscribe":
			Log("codex-unsubscribe", fmt.Sprint(params["threadId"]))
			reply(map[string]any{})
		case "account/rateLimits/read":
			Log("codex-ratelimits", string(m.Params))
			reply(map[string]any{"rateLimits": map[string]any{"primary": map[string]any{"usedPercent": 5, "windowDurationMins": 300, "resetsAt": 1}}})
		case "thread/goal/set":
			_, marker := params["jaraDriver"]
			Log("codex-goal", fmt.Sprint(params["threadId"], "|", params["objective"], "|", params["status"], "|marker=", marker))
			reply(map[string]any{"goal": params})
		case "thread/list":
			reply(map[string]any{"data": []any{map[string]any{"id": "codex-thread-1", "cwd": "/w"}, map[string]any{"id": "codex-thread-2", "cwd": "/w"}, map[string]any{"id": "codex-other", "cwd": "/w"}}, "nextCursor": nil})
		case "turn/start":
			text := params["input"].([]any)[0].(map[string]any)["text"].(string)
			Log("codex-turn", fmt.Sprint(params["threadId"], "|", text))
			thread := params["threadId"]
			reply(map[string]any{"turn": map[string]any{"id": "ct"}})
			for _, n := range []map[string]any{
				{"method": "turn/started", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": "ct"}}},
				{"method": "item/completed", "params": map[string]any{"threadId": thread, "item": map[string]any{"id": "m", "type": "agentMessage", "text": "codex: " + LastLine(text)}}},
				{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": "ct", "status": "completed"}}},
			} {
				_ = out.Encode(n)
			}
		default:
			_ = out.Encode(map[string]any{"id": m.ID, "error": map[string]any{"message": "unsupported " + m.Method}})
		}
	}
}
