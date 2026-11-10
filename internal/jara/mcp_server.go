package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
)

// Jara's MCP server speaks newline-delimited JSON-RPC 2.0 over stdio. Both
// Codex and Claude Code launch it, so either agent can delegate to the other.
var mcpProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mcpCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("use jara mcp (started by Codex or Claude Code over stdio)")
	}
	return serveMCP(os.Stdin, os.Stdout)
}

// serveMCP handles requests concurrently so a long tool call that waits for a
// task does not block pings or other calls.
func serveMCP(input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	var writeMu sync.Mutex
	var pending sync.WaitGroup
	write := func(response mcpResponse) {
		response.JSONRPC = "2.0"
		data, err := json.Marshal(response)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, _ = output.Write(append(data, '\n'))
	}
	for scanner.Scan() {
		var message mcpMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			write(mcpResponse{ID: json.RawMessage("null"), Error: &mcpError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(message.ID) == 0 || string(message.ID) == "null" {
			continue // notifications need no response
		}
		pending.Add(1)
		go func(message mcpMessage) {
			defer pending.Done()
			result, err := handleMCP(message)
			if err != nil {
				write(mcpResponse{ID: message.ID, Error: err})
				return
			}
			write(mcpResponse{ID: message.ID, Result: result})
		}(message)
	}
	pending.Wait()
	return scanner.Err()
}

func handleMCP(message mcpMessage) (any, *mcpError) {
	switch message.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(message.Params, &params)
		version := mcpProtocolVersions[0]
		if slices.Contains(mcpProtocolVersions, params.ProtocolVersion) {
			version = params.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "jara", "title": "Jara agent delegation", "version": Version},
			"instructions":    mcpInstructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": delegationTools()}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return nil, &mcpError{Code: -32602, Message: "invalid tool call"}
		}
		if len(params.Arguments) == 0 {
			params.Arguments = json.RawMessage("{}")
		}
		text, err := callDelegationTool(params.Name, params.Arguments)
		if err != nil {
			return map[string]any{"content": []map[string]string{{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}, nil
	default:
		return nil, &mcpError{Code: -32601, Message: "method not found: " + message.Method}
	}
}

const mcpInstructions = "Jara runs coding agents on this host. Use delegate_task to hand a self-contained job to Claude Code or Codex " +
	"(including another instance of yourself). By default it works in its own Git worktree branched from your HEAD, so your uncommitted " +
	"changes are not visible to it; review its result with task_status, ask follow-ups with continue_task, and bring its work into your " +
	"checkout with merge_task. People can watch every task with `jara sessions`."

func schema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func delegationTools() []map[string]any {
	str := func(description string, values ...string) map[string]any {
		property := map[string]any{"type": "string", "description": description}
		if len(values) > 0 {
			property["enum"] = values
		}
		return property
	}
	wait := map[string]any{"type": "integer", "minimum": 0, "maximum": maxTaskWait, "description": "Seconds to wait for the run to finish before returning (default 45). Poll task_status for longer work."}
	taskID := str("Task ID returned by delegate_task.")
	return []map[string]any{
		{"name": "delegate_task", "title": "Delegate to an agent",
			"description": "Start Claude Code or Codex on a self-contained task in this workspace and return its result, or a task ID if it is still running. Write the prompt as a complete brief: the other agent cannot see your conversation.",
			"inputSchema": schema(map[string]any{
				"agent":        str("Agent to run.", agentClaude, agentCodex),
				"prompt":       str("Complete instructions for the task."),
				"isolation":    str("worktree (default for Git repositories): own branch and checkout from your HEAD; shared: work directly in your checkout.", "worktree", "shared"),
				"permission":   str("read-only, edit (default: edit files, commands that need approval are refused), or full (no restrictions inside the container).", permissionReadOnly, permissionEdit, permissionFull),
				"model":        str("Optional model name for the agent."),
				"cwd":          str("Optional absolute working directory; defaults to the directory the agent was started in."),
				"wait_seconds": wait,
			}, "agent", "prompt")},
		{"name": "task_status", "title": "Task status", "description": "Report a task's status and result, waiting up to wait_seconds for a running task.",
			"inputSchema": schema(map[string]any{"task_id": taskID, "wait_seconds": wait}, "task_id")},
		{"name": "continue_task", "title": "Continue a task", "description": "Send a follow-up prompt to a finished task's agent conversation.",
			"inputSchema": schema(map[string]any{"task_id": taskID, "prompt": str("Follow-up instructions."), "wait_seconds": wait}, "task_id", "prompt")},
		{"name": "list_tasks", "title": "List tasks", "description": "List recent delegated tasks on this host.",
			"inputSchema": schema(map[string]any{})},
		{"name": "merge_task", "title": "Merge a task", "description": "Commit a finished worktree task's changes on its branch and merge it into your checkout.",
			"inputSchema": schema(map[string]any{"task_id": taskID, "cleanup": map[string]any{"type": "boolean", "description": "Remove the task worktree and branch after a successful merge (default true)."}}, "task_id")},
		{"name": "cancel_task", "title": "Cancel a task", "description": "Stop a running task.",
			"inputSchema": schema(map[string]any{"task_id": taskID}, "task_id")},
	}
}
