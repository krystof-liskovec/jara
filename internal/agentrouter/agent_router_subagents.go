package agentrouter

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// Subagents can be viewed and messaged from the interface. Codex runs each
// subagent in its own thread, so the router only passes those requests on.
// Claude Code reports a subagent's messages inside the parent's stream, keyed
// by the Task/Agent tool call that started it; the router collects them into
// a transcript served as a virtual thread.

const claudeSubagentOutputLimit = 4000

type claudeSubagent struct {
	id, toolUseID, parentThread string
	name, prompt, status        string
	agentID                     string // Claude Code's agent ID, once reported
	background                  bool
	started                     time.Time
	items                       []map[string]any
	tools                       map[string]int // nested tool_use ID -> index in items
}

func claudeSubagentTool(name string) bool { return name == "Task" || name == "Agent" }

func (r *agentRouter) noteCodexSubagent(threadID string) {
	if threadID == "" {
		return
	}
	if r.codexChildren == nil {
		r.codexChildren = map[string]bool{}
	}
	r.codexChildren[threadID] = true
}

// observeCodexSubagents remembers Codex subagent threads so messages to them
// go straight to Codex instead of starting a conversation of their own.
func (r *agentRouter) observeCodexSubagents(m rpcMessage) {
	var p struct {
		Item struct {
			Type              string   `json:"type"`
			AgentThreadID     string   `json:"agentThreadId"`
			ReceiverThreadIDs []string `json:"receiverThreadIds"`
		} `json:"item"`
		Thread struct {
			ID             string `json:"id"`
			ParentThreadID string `json:"parentThreadId"`
		} `json:"thread"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	switch {
	case m.Method == "thread/started" && p.Thread.ParentThreadID != "":
		r.noteCodexSubagent(p.Thread.ID)
	case p.Item.Type == "subAgentActivity":
		r.noteCodexSubagent(p.Item.AgentThreadID)
	case p.Item.Type == "collabAgentToolCall":
		for _, id := range p.Item.ReceiverThreadIDs {
			r.noteCodexSubagent(id)
		}
	}
}

// subagentTurn reports whether a turn/start targets a subagent's thread, and
// returns the parameters to forward without Jara's marker.
func (r *agentRouter) subagentTurn(params json.RawMessage) (json.RawMessage, string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil {
		return params, "", false
	}
	var thread string
	_ = json.Unmarshal(fields["threadId"], &thread)
	var marked bool
	_ = json.Unmarshal(fields["jaraSubagent"], &marked)
	if !marked && !r.codexChildren[thread] && !strings.HasPrefix(thread, protocol.ClaudeSubagentThreadPrefix) {
		return params, "", false
	}
	delete(fields, "jaraSubagent")
	forward, err := json.Marshal(fields)
	if err != nil {
		return params, thread, true
	}
	r.noteCodexSubagent(thread)
	return forward, thread, true
}

func (r *agentRouter) claudeSubagentFor(params json.RawMessage) *claudeSubagent {
	var request struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &request) != nil {
		return nil
	}
	return r.claudeAgents[request.ThreadID]
}

// startClaudeSubagent registers the subagent a Task/Agent call starts and
// returns the items announcing it.
func (s *claudeSession) startClaudeSubagent(toolUseID string, input map[string]any) (*claudeSubagent, map[string]any, map[string]any) {
	r := s.r
	if r.claudeAgents == nil {
		r.claudeAgents = map[string]*claudeSubagent{}
	}
	name := textutil.FirstNonEmpty(stringField(input, "description"), stringField(input, "subagent_type"), "subagent")
	agent := &claudeSubagent{
		id: protocol.ClaudeSubagentThreadPrefix + toolUseID, toolUseID: toolUseID, parentThread: s.thread.ID,
		name: name, prompt: stringField(input, "prompt"), status: "running", started: time.Now(),
		background: input["run_in_background"] == true, tools: map[string]int{},
	}
	if agent.prompt != "" {
		agent.items = append(agent.items, map[string]any{"id": toolUseID + "-prompt", "type": "userMessage",
			"content": []map[string]any{{"type": "text", "text": agent.prompt}}})
	}
	r.claudeAgents[agent.id] = agent
	activity := map[string]any{"id": toolUseID + "-activity", "type": "subAgentActivity", "kind": "started",
		"agentThreadId": agent.id, "agentPath": name}
	call := map[string]any{"id": toolUseID, "type": "collabAgentToolCall", "tool": "spawnAgent", "status": "inProgress",
		"senderThreadId": s.thread.ID, "receiverThreadIds": []string{agent.id}, "prompt": agent.prompt,
		"agentsStates": map[string]any{agent.id: map[string]any{"status": "running"}}}
	return agent, activity, call
}

// finishClaudeSubagent records the Task/Agent result. A background subagent
// keeps running until Claude Code reports its task notification.
func (s *claudeSession) finishClaudeSubagent(tool *claudeTool, content string, failed bool) {
	agent := s.r.claudeAgents[protocol.ClaudeSubagentThreadPrefix+tool.id]
	if agent == nil {
		return
	}
	status, kind := "completed", "completed"
	switch {
	case failed:
		status, kind = "errored", "interrupted"
	case agent.background:
		status, kind = "running", ""
	}
	agent.status = status
	if agent.agentID == "" {
		agent.agentID = claudeAgentIDFromResult(content)
	}
	if content != "" && !agent.background {
		agent.items = append(agent.items, map[string]any{"id": tool.id + "-result", "type": "agentMessage",
			"text": textutil.TailText(content, claudeSubagentOutputLimit)})
	}
	tool.item["agentsStates"] = map[string]any{agent.id: map[string]any{"status": status, "message": textutil.TailText(content, 600)}}
	if kind != "" {
		s.r.notify("item/completed", map[string]any{"threadId": s.thread.ID, "turnId": s.turnID, "item": map[string]any{
			"id": tool.id + "-activity-end", "type": "subAgentActivity", "kind": kind, "agentThreadId": agent.id, "agentPath": agent.name}})
	}
	s.r.notifySubagent(agent)
}

// claudeAgentIDFromResult reads the "agentId: …" line Claude Code appends to
// a subagent's result, used to address it with SendMessage.
func claudeAgentIDFromResult(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"agentId:", "agent_id:", "Agent ID:"} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				if fields := strings.Fields(rest); len(fields) > 0 {
					return strings.Trim(fields[0], "`.,;()")
				}
			}
		}
	}
	return ""
}

// recordNestedClaudeLine adds a subagent's message to its transcript.
func (s *claudeSession) recordNestedClaudeLine(line claudeLine) {
	agent := s.r.claudeAgents[protocol.ClaudeSubagentThreadPrefix+*line.ParentToolUseID]
	if agent == nil {
		return
	}
	if line.AgentID != "" {
		agent.agentID = line.AgentID
	}
	var blocks []claudeContentBlock
	if json.Unmarshal(line.Message.Content, &blocks) != nil {
		// A plain-text user message, such as one sent with SendMessage.
		blocks = []claudeContentBlock{{Type: "text", Text: contentText(line.Message.Content)}}
	}
	changed := false
	for _, block := range blocks {
		switch {
		case line.Type == "assistant" && block.Type == "text" && strings.TrimSpace(block.Text) != "":
			agent.items = append(agent.items, map[string]any{"id": agent.toolUseID + "-text-" + strconv.Itoa(len(agent.items)), "type": "agentMessage", "text": block.Text})
			changed = true
		case line.Type == "user" && block.Type == "text" && strings.TrimSpace(block.Text) != "" && len(agent.items) > 1:
			// The first user message repeats the Task prompt already shown.
			agent.items = append(agent.items, map[string]any{"id": agent.toolUseID + "-user-" + strconv.Itoa(len(agent.items)),
				"type": "userMessage", "content": []map[string]any{{"type": "text", "text": block.Text}}})
			changed = true
		case line.Type == "assistant" && block.Type == "tool_use":
			command := claudeToolLabel(block.Name, block.Input)
			if block.Name == "Bash" {
				command = stringField(block.Input, "command")
			}
			if claudeSubagentTool(block.Name) {
				// A subagent of this subagent: viewable in its own right.
				nested, _, _ := s.startClaudeSubagent(block.ID, block.Input)
				nested.parentThread = agent.id
			}
			agent.tools[block.ID] = len(agent.items)
			agent.items = append(agent.items, map[string]any{"id": block.ID, "type": "commandExecution", "command": command, "status": "inProgress"})
			changed = true
		case line.Type == "user" && block.Type == "tool_result":
			index, ok := agent.tools[block.ToolUseID]
			if !ok {
				continue
			}
			status := "completed"
			if block.IsError {
				status = "failed"
			}
			agent.items[index]["status"] = status
			agent.items[index]["aggregatedOutput"] = textutil.TailText(contentText(block.Content), claudeSubagentOutputLimit)
			delete(agent.tools, block.ToolUseID)
			if nested := s.r.claudeAgents[protocol.ClaudeSubagentThreadPrefix+block.ToolUseID]; nested != nil && nested.status == "running" && !nested.background {
				nested.status = map[bool]string{true: "errored", false: "completed"}[block.IsError]
			}
			changed = true
		}
	}
	if changed {
		s.r.notifySubagent(agent)
	}
}

// finishBackgroundClaudeSubagent applies a task notification for a subagent
// that ran in the background.
func (s *claudeSession) finishBackgroundClaudeSubagent(line claudeLine) {
	agent := s.r.claudeAgents[protocol.ClaudeSubagentThreadPrefix+line.ToolUseID]
	if line.ToolUseID == "" || agent == nil {
		return
	}
	agent.status = map[string]string{"completed": "completed", "failed": "errored", "stopped": "shutdown"}[line.Status]
	if agent.status == "" {
		agent.status = "completed"
	}
	if summary := strings.TrimSpace(line.Summary); summary != "" {
		agent.items = append(agent.items, map[string]any{"id": agent.toolUseID + "-summary", "type": "agentMessage", "text": summary})
	}
	s.r.notifySubagent(agent)
}

// notifySubagent tells the interface a subagent's transcript or status changed.
func (r *agentRouter) notifySubagent(agent *claudeSubagent) {
	r.notify("jara/subagent/updated", map[string]any{"threadId": agent.id, "parentThreadId": agent.parentThread,
		"name": agent.name, "status": agent.status, "agentId": agent.agentID})
}

// threadRead answers thread/read for a Claude Code subagent.
func (agent *claudeSubagent) threadRead() map[string]any {
	status := map[string]any{"type": "idle"}
	if agent.status == "running" {
		status = map[string]any{"type": "active", "activeFlags": []string{}}
	}
	started := agent.started.Unix()
	return map[string]any{"thread": map[string]any{
		"id": agent.id, "parentThreadId": agent.parentThread, "agentNickname": agent.name, "status": status,
		"jaraStatus": agent.status, "jaraAgentId": agent.agentID,
		"turns": []map[string]any{{"id": agent.toolUseID, "startedAt": started, "items": agent.items}},
	}}
}
