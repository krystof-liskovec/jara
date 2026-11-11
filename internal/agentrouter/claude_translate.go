package agentrouter

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

const claudeToolOutputLimit = 6000

type claudeLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Event     struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Message         struct {
		Content json.RawMessage `json:"content"`
		Usage   *claudeUsage    `json:"usage"`
	} `json:"message"`
	ModelUsage map[string]struct {
		ContextWindow int64 `json:"contextWindow"`
	} `json:"modelUsage"`
	ToolUseResult json.RawMessage `json:"tool_use_result"`
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	Result        string          `json:"result"`
	Usage         struct {
		Input         int64 `json:"input_tokens"`
		Output        int64 `json:"output_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	EstimatedTokens int64    `json:"estimated_tokens"`
	Model           string   `json:"model"` // init: the model Claude Code resolved
	IsError         bool     `json:"is_error"`
	Errors          []string `json:"errors"`
}

type claudeContentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (s *claudeSession) handleLine(raw []byte) {
	var line claudeLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	// Subagent (Task) traffic is summarized by its parent tool call.
	nested := line.ParentToolUseID != nil && *line.ParentToolUseID != ""
	switch line.Type {
	case "stream_event":
		if nested || s.turnID == "" {
			return
		}
		switch line.Event.Type {
		case "content_block_start":
			if line.Event.ContentBlock.Type == "text" {
				s.openTextItem()
			}
		case "content_block_delta":
			if line.Event.Delta.Type == "text_delta" && line.Event.Delta.Text != "" {
				s.openTextItem()
				s.blockText.WriteString(line.Event.Delta.Text)
				s.reply.WriteString(line.Event.Delta.Text)
				s.r.notify("item/agentMessage/delta", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "itemId": s.textItem, "delta": line.Event.Delta.Text})
			}
		case "content_block_stop":
			s.closeTextItem()
		}
	case "assistant":
		if nested {
			return
		}
		if usage := line.Message.Usage; usage != nil && s.turnID != "" {
			s.contextTokens = usage.Input + usage.CacheRead + usage.CacheCreation + usage.Output
			s.reportContext()
		}
		var blocks []claudeContentBlock
		_ = json.Unmarshal(line.Message.Content, &blocks)
		for _, block := range blocks {
			if block.Type == "tool_use" {
				s.toolStarted(block.ID, block.Name, block.Input)
			}
		}
	case "user":
		if nested {
			return
		}
		var blocks []claudeContentBlock
		_ = json.Unmarshal(line.Message.Content, &blocks)
		for _, block := range blocks {
			if block.Type == "tool_result" {
				s.toolFinished(block.ToolUseID, contentText(block.Content), block.IsError, line.ToolUseResult)
			}
		}
	case "control_request":
		s.handleControlRequest(line.RequestID, line.Request)
	case "rate_limit_event":
		s.r.observeClaudeRateLimits(line.RateLimitInfo)
	case "system":
		if line.Subtype == "init" && line.Model != "" && line.Model != s.resolvedModel {
			s.resolvedModel = line.Model
			s.r.notify("jara/model/resolved", map[string]any{"threadId": s.thread.ID, "model": line.Model})
		}
		// Claude Code does not expose thinking text in stream-json mode, only
		// a running estimate; show that as live progress.
		if line.Subtype == "thinking_tokens" && line.EstimatedTokens > 0 && s.turnID != "" && !nested {
			s.r.notify("jara/activity", map[string]any{"threadId": s.thread.ID, "label": fmt.Sprintf("Thinking · ~%d tokens", line.EstimatedTokens)})
		}
	case "result":
		s.tokens += line.Usage.Input + line.Usage.Output + line.Usage.CacheCreation
		for _, usage := range line.ModelUsage {
			s.contextWindow = max(s.contextWindow, usage.ContextWindow)
		}
		s.reportContext()
		switch {
		case s.interrupted:
			s.finishTurn("interrupted", "")
		case line.Subtype == "success" && !line.IsError:
			s.finishTurn("completed", "")
		default:
			message := strings.TrimSpace(line.Result)
			if message == "" {
				message = strings.Join(line.Errors, "; ")
			}
			if message == "" {
				message = "Claude Code ended the turn with " + line.Subtype
			}
			if s.r.claudeUsageLimited(message) {
				s.usageLimited = true
				message = "Claude usage limit reached: " + message
			}
			s.finishTurn("failed", message)
		}
	}
}

func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var out []string
	for _, part := range parts {
		if part.Type == "text" {
			out = append(out, part.Text)
		}
	}
	return strings.Join(out, "\n")
}

func (s *claudeSession) nextItemID(kind string) string {
	s.itemCount++
	return s.backendID + "-" + kind + "-" + strconv.Itoa(s.itemCount)
}

func (s *claudeSession) openTextItem() {
	if s.textItem != "" {
		return
	}
	if s.reply.Len() > 0 {
		s.reply.WriteString("\n\n")
		s.r.notify("item/agentMessage/delta", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "itemId": s.nextItemID("gap"), "delta": "\n\n"})
	}
	s.textItem = s.nextItemID("message")
	s.blockText.Reset()
	s.r.notify("item/started", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "item": map[string]any{"id": s.textItem, "type": "agentMessage", "text": ""}})
}

func (s *claudeSession) closeTextItem() {
	if s.textItem == "" {
		return
	}
	s.r.notify("item/completed", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "item": map[string]any{"id": s.textItem, "type": "agentMessage", "text": s.blockText.String()}})
	s.textItem = ""
}

func claudeFileTool(name string) bool {
	return name == "Edit" || name == "MultiEdit" || name == "Write" || name == "NotebookEdit"
}

func stringField(input map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := input[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// claudeToolLabel describes non-shell tools in the transcript, e.g. Read(main.go).
func claudeToolLabel(name string, input map[string]any) string {
	detail := stringField(input, "file_path", "notebook_path", "path", "pattern", "url", "query", "description", "skill", "prompt")
	if len(detail) > 120 {
		detail = detail[:117] + "…"
	}
	if detail == "" {
		return name
	}
	return name + "(" + detail + ")"
}

func (s *claudeSession) toolStarted(id, name string, input map[string]any) *claudeTool {
	if tool, ok := s.tools[id]; ok {
		return tool
	}
	tool := &claudeTool{id: id, name: name, input: input}
	switch {
	case name == "Bash":
		tool.item = map[string]any{"id": id, "type": "commandExecution", "command": stringField(input, "command"), "status": "inProgress"}
	case claudeFileTool(name):
		path := relativeToWorkspace(stringField(input, "file_path", "notebook_path"), s.thread.CWD)
		tool.item = map[string]any{"id": id, "type": "fileChange", "status": "inProgress", "changes": []map[string]any{{"path": path, "kind": "update"}}}
	default:
		tool.item = map[string]any{"id": id, "type": "commandExecution", "command": claudeToolLabel(name, input), "status": "inProgress"}
	}
	s.tools[id] = tool
	if s.turnID != "" {
		s.r.notify("item/started", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "item": tool.item})
	}
	return tool
}

func (s *claudeSession) toolFinished(id, content string, failed bool, result json.RawMessage) {
	tool, ok := s.tools[id]
	if !ok || s.turnID == "" {
		return
	}
	status := "completed"
	if failed {
		status = "failed"
	}
	item := tool.item
	item["status"] = status
	switch {
	case tool.name == "Bash":
		var output struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		_ = json.Unmarshal(result, &output)
		text := strings.TrimRight(output.Stdout+output.Stderr, "\n")
		if text == "" {
			text = content
		}
		item["aggregatedOutput"] = text
		if turn := s.thread.lastTurn(); turn != nil {
			exit := 0
			if failed {
				exit = 1
			}
			turn.addAction(commandSummary(stringField(tool.input, "command"), &exit, text))
		}
		if text != "" {
			s.r.notify("item/commandExecution/outputDelta", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "itemId": id, "delta": textutil.TailText(text, claudeToolOutputLimit) + "\n"})
		}
	case claudeFileTool(tool.name) && !failed:
		changes := claudeFileChanges(tool, result, s.thread.CWD)
		item["changes"] = changes
		if turn := s.thread.lastTurn(); turn != nil {
			turn.Changes = append(turn.Changes, changes...)
		}
	case failed:
		s.r.notify("item/commandExecution/outputDelta", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "itemId": id, "delta": textutil.TailText(content, 2000) + "\n"})
	}
	s.r.notify("item/completed", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "item": item})
	delete(s.tools, id)
}

// claudeFileChanges converts Claude Code's structured patch into the unified
// diff shape the conversation UI and changes pane render.
func claudeFileChanges(tool *claudeTool, raw json.RawMessage, cwd string) []protocol.FileChange {
	var result struct {
		Type     string `json:"type"`
		FilePath string `json:"filePath"`
		Content  string `json:"content"`
		Patch    []struct {
			OldStart int      `json:"oldStart"`
			OldLines int      `json:"oldLines"`
			NewStart int      `json:"newStart"`
			NewLines int      `json:"newLines"`
			Lines    []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	_ = json.Unmarshal(raw, &result)
	path := relativeToWorkspace(textutil.FirstNonEmpty(result.FilePath, stringField(tool.input, "file_path", "notebook_path")), cwd)
	var diff strings.Builder
	kind := "update"
	if result.Type == "create" {
		kind = "add"
		lines := strings.Split(strings.TrimSuffix(result.Content, "\n"), "\n")
		fmt.Fprintf(&diff, "@@ -0,0 +1,%d @@\n", len(lines))
		for _, line := range lines {
			diff.WriteString("+" + line + "\n")
		}
	}
	for _, hunk := range result.Patch {
		fmt.Fprintf(&diff, "@@ -%d,%d +%d,%d @@\n", hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)
		for _, line := range hunk.Lines {
			diff.WriteString(line + "\n")
		}
	}
	if diff.Len() == 0 {
		if oldText, newText := stringField(tool.input, "old_string"), stringField(tool.input, "new_string"); oldText != "" || newText != "" {
			diff.WriteString("@@ edit @@\n")
			for _, line := range strings.Split(oldText, "\n") {
				diff.WriteString("-" + line + "\n")
			}
			for _, line := range strings.Split(newText, "\n") {
				diff.WriteString("+" + line + "\n")
			}
		}
	}
	kindJSON, _ := json.Marshal(kind)
	return []protocol.FileChange{{Path: path, Kind: kindJSON, Diff: diff.String()}}
}

func (s *claudeSession) handleControlRequest(requestID string, raw json.RawMessage) {
	var request struct {
		Subtype     string         `json:"subtype"`
		ToolName    string         `json:"tool_name"`
		Input       map[string]any `json:"input"`
		ToolUseID   string         `json:"tool_use_id"`
		Description string         `json:"description"`
	}
	_ = json.Unmarshal(raw, &request)
	if request.Subtype != "can_use_tool" {
		_ = s.write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": requestID, "error": "unsupported by Jara: " + request.Subtype}})
		return
	}
	if request.ToolName == "AskUserQuestion" || s.turnID == "" {
		s.respondPermission(requestID, nil, false, "Ask the question in your reply instead; the user answers in their next message.")
		return
	}
	toolID := textutil.FirstNonEmpty(request.ToolUseID, s.nextItemID("tool"))
	tool := s.toolStarted(toolID, request.ToolName, request.Input)
	s.r.sequence++
	uiID := strconv.Quote("claude-approval-" + strconv.Itoa(s.r.sequence))
	s.approvals[uiID] = claudeApproval{requestID: requestID, input: request.Input}
	s.r.approvals[uiID] = s
	method, params := "item/commandExecution/requestApproval", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "itemId": tool.id, "reason": request.Description}
	if claudeFileTool(request.ToolName) {
		method = "item/fileChange/requestApproval"
	} else {
		params["command"] = tool.item["command"]
	}
	s.r.send(map[string]any{"id": json.RawMessage(uiID), "method": method, "params": params})
}

func (s *claudeSession) answerApproval(uiID string, result json.RawMessage) {
	approval, ok := s.approvals[uiID]
	if !ok {
		return
	}
	delete(s.approvals, uiID)
	var answer struct {
		Decision string `json:"decision"`
	}
	_ = json.Unmarshal(result, &answer)
	allow := answer.Decision == "accept" || answer.Decision == "acceptForSession" || answer.Decision == "approved"
	s.respondPermission(approval.requestID, approval.input, allow, "The user declined this action.")
}

func (s *claudeSession) respondPermission(requestID string, input map[string]any, allow bool, message string) {
	response := map[string]any{"behavior": "deny", "message": message}
	if allow {
		if input == nil {
			input = map[string]any{}
		}
		response = map[string]any{"behavior": "allow", "updatedInput": input}
	}
	_ = s.write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": requestID, "response": response}})
}

// relativeToWorkspace shortens absolute paths inside the conversation's
// working directory, matching how Codex reports changes.
func relativeToWorkspace(path, cwd string) string {
	if cwd == "" || !filepath.IsAbs(path) {
		return path
	}
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

type claudeUsage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
}

// reportContext tells the UI how full Claude Code's context is, in the shape
// of Codex's token usage notification.
func (s *claudeSession) reportContext() {
	if s.contextTokens <= 0 || s.turnID == "" {
		return
	}
	window := s.contextWindow
	if window == 0 {
		window = 200000
		if strings.HasSuffix(textutil.FirstNonEmpty(s.model, s.resolvedModel), "[1m]") {
			window = 1000000
		}
	}
	last := map[string]any{"totalTokens": s.contextTokens}
	s.r.notify("thread/tokenUsage/updated", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID,
		"tokenUsage": map[string]any{"last": last, "total": last, "modelContextWindow": window}})
}
