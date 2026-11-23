package jara

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// The interface lists the conversation's subagents, shows any one's
// transcript in place of the main one, and sends it messages. Codex runs each
// subagent in its own thread; Claude Code subagents are virtual threads the
// agent router serves, and messages to them are relayed by the main agent.

const (
	chatAgentPollInterval = 1500 * time.Millisecond
	// Running agents not on screen are checked less often, so one whose
	// completion event never arrived is not shown as running forever.
	chatAgentStatusInterval = 5 * time.Second
	// Finished agents leave the strip above the composer after this long;
	// /agents still lists them.
	chatAgentLinger = 30 * time.Second
)

type chatAgent struct {
	id, name, status string
	agentID          string // Claude Code's ID for SendMessage, when known
	started          time.Time
	finished         time.Time // when it stopped running; zero while it runs
	turns            []protocol.HistoryTurn
	readError        string
	notes            []string // messages sent since the last read
	version          int      // changes whenever the transcript does
	stale, reading   bool
	lastRead         time.Time
}

type chatAgentsState struct {
	agents    []*chatAgent
	agentByID map[string]*chatAgent
	viewing   string // the agent whose transcript is shown; "" for the main agent
	// agentViewRequest asks the terminal to show an agent ("main" for the
	// main agent), set by /agents N.
	agentViewRequest string
}

func (a *chatAgent) claude() bool {
	return strings.HasPrefix(a.id, protocol.ClaudeSubagentThreadPrefix)
}

func (a *chatAgent) running() bool { return a.status == "running" || a.status == "starting" }

func (a *chatAgent) touch() { a.version++ }

// chatAgentStatus normalizes Codex and router statuses for display.
func chatAgentStatus(status string) string {
	switch status {
	case "inProgress", "in_progress", "running", "active", "started", "interacted":
		return "running"
	case "pendingInit", "pending_init":
		return "starting"
	case "errored", "failed", "systemError":
		return "failed"
	case "shutdown", "stopped":
		return "stopped"
	case "interrupted":
		return "interrupted"
	case "completed", "idle":
		return "completed"
	case "notFound", "not_found":
		return "not found"
	}
	return ""
}

func (u *chatUI) chatAgentFor(id string) *chatAgent {
	if id == "" || id == u.thread {
		return nil
	}
	if u.agentByID == nil {
		u.agentByID = map[string]*chatAgent{}
	}
	agent := u.agentByID[id]
	if agent == nil {
		agent = &chatAgent{id: id, name: chatSubagentName(id, "", u.subagentPaths), status: "running", started: time.Now(), stale: true}
		u.agentByID[id] = agent
		u.agents = append(u.agents, agent)
	}
	return agent
}

func (u *chatUI) setAgentStatus(agent *chatAgent, status string) {
	u.setAgentStatusAt(agent, status, time.Now())
}

// setAgentStatusAt applies a status reported at, which is in the past for
// replayed history.
func (u *chatUI) setAgentStatusAt(agent *chatAgent, status string, at time.Time) {
	if status = chatAgentStatus(status); status != "" && status != agent.status {
		agent.status = status
		agent.finished = time.Time{}
		if !agent.running() {
			agent.finished = at
		}
		agent.stale = true
		agent.touch()
	}
}

// onStrip reports whether the strip above the composer shows the agent:
// while it runs, and for a while after it finished.
func (a *chatAgent) onStrip(now time.Time) bool {
	return a.running() || now.Sub(a.finished) < chatAgentLinger
}

// noteSubagentItem registers the subagents a parent-thread item mentions.
func (u *chatUI) noteSubagentItem(item protocol.HistoryItem, at time.Time) {
	switch item.Type {
	case "subAgentActivity":
		agent := u.chatAgentFor(item.AgentThreadID)
		if agent == nil {
			return
		}
		if item.AgentPath != "" {
			agent.name = item.AgentPath
		}
		if item.Kind == "started" && !at.IsZero() {
			agent.started = at
		}
		u.setAgentStatusAt(agent, item.Kind, cmp.Or(at, time.Now()))
	case "collabAgentToolCall":
		spawn := item.Tool == "spawnAgent" || item.Tool == "spawn_agent"
		for _, id := range item.ReceiverThreadIDs {
			if u.agentByID[id] == nil && !spawn {
				continue
			}
			if agent := u.chatAgentFor(id); agent != nil && spawn && !at.IsZero() && item.Status != "completed" {
				agent.started = at
			}
		}
		for id, state := range item.AgentsStates {
			if agent := u.chatAgentFor(id); agent != nil {
				u.setAgentStatusAt(agent, state.Status, cmp.Or(at, time.Now()))
			}
		}
	}
}

// routeChatAgentEvent handles a notification about a subagent's own thread,
// keeping it out of the main conversation. It reports whether it did.
func (u *chatUI) routeChatAgentEvent(m appMessage) bool {
	if len(m.ID) > 0 || len(m.Params) == 0 {
		return false // server requests, such as approvals, still go to the main view
	}
	var p struct {
		ThreadID string          `json:"threadId"`
		Name     string          `json:"name"`
		Status   json.RawMessage `json:"status"`
		AgentID  string          `json:"agentId"`
		Turn     struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return false
	}
	if m.Method == "jara/subagent/updated" {
		var status string
		_ = json.Unmarshal(p.Status, &status)
		agent := u.chatAgentFor(p.ThreadID)
		if agent == nil {
			return true
		}
		if p.Name != "" {
			agent.name = p.Name
		}
		if p.AgentID != "" {
			agent.agentID = p.AgentID
		}
		u.setAgentStatus(agent, status)
		agent.stale = true
		return true
	}
	agent := u.agentByID[p.ThreadID]
	if agent == nil || p.ThreadID == u.thread {
		return false
	}
	switch m.Method {
	case "turn/started":
		u.setAgentStatus(agent, "running")
	case "turn/completed":
		u.setAgentStatus(agent, p.Turn.Status)
	case "thread/status/changed":
		var status struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(p.Status, &status)
		u.applyAgentThreadStatus(agent, "", status.Type)
	}
	agent.stale = true
	return true
}

func (u *chatUI) viewedAgent() *chatAgent {
	if u.viewing == "" {
		return nil
	}
	return u.agentByID[u.viewing]
}

// pollChatAgent keeps the viewed agent's transcript current, and checks
// the status of the other running agents.
func (u *chatUI) pollChatAgent(now time.Time) error {
	if u.chatOffline() {
		return nil
	}
	for _, agent := range u.agents {
		if agent.reading || agent.id == u.viewing || !agent.running() || now.Sub(agent.lastRead) < chatAgentStatusInterval {
			continue
		}
		agent.reading, agent.lastRead = true, now
		if err := u.requestWithAction("thread/read", map[string]any{"threadId": agent.id, "includeTurns": false}, "agentstatus:"+agent.id); err != nil {
			return err
		}
	}
	agent := u.viewedAgent()
	if agent == nil || agent.reading {
		return nil
	}
	if !agent.stale && !(agent.running() && now.Sub(agent.lastRead) >= chatAgentPollInterval) {
		return nil
	}
	agent.reading, agent.stale, agent.lastRead = true, false, now
	return u.requestWithAction("thread/read", map[string]any{"threadId": agent.id, "includeTurns": true}, "agent:"+agent.id)
}

// applyAgentThreadStatus applies the status a thread reports: Jara's own
// for a Claude Code subagent, otherwise Codex's thread status. A thread that
// is idle, or no longer loaded, is not running.
func (u *chatUI) applyAgentThreadStatus(agent *chatAgent, jaraStatus, threadStatus string) {
	switch {
	case jaraStatus != "":
		u.setAgentStatus(agent, jaraStatus)
	case threadStatus == "active":
		u.setAgentStatus(agent, "running")
	case threadStatus == "systemError":
		u.setAgentStatus(agent, "failed")
	case (threadStatus == "idle" || threadStatus == "notLoaded") && agent.running():
		u.setAgentStatus(agent, "completed")
	}
}

// acceptChatAgentStatus handles the reply to a status check.
func (u *chatUI) acceptChatAgentStatus(agent *chatAgent, m appMessage) {
	agent.reading = false
	var r struct {
		Thread struct {
			JaraStatus string `json:"jaraStatus"`
			Status     struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if m.Error != nil || json.Unmarshal(m.Result, &r) != nil {
		return // checked again later
	}
	u.applyAgentThreadStatus(agent, r.Thread.JaraStatus, r.Thread.Status.Type)
}

// acceptChatAgentResponse handles replies to subagent reads and messages.
func (u *chatUI) acceptChatAgentResponse(m appMessage, p chatPending) bool {
	if id, ok := strings.CutPrefix(p.action, "agentstatus:"); ok {
		if agent := u.agentByID[id]; agent != nil {
			u.acceptChatAgentStatus(agent, m)
		}
		return true
	}
	id, ok := strings.CutPrefix(p.action, "agent:")
	if !ok {
		return false
	}
	agent := u.agentByID[id]
	if agent == nil {
		return true
	}
	switch p.method {
	case "thread/read":
		agent.reading = false
		if m.Error != nil {
			agent.readError = chatText(m.Error.Message)
			agent.touch()
			return true
		}
		var r struct {
			Thread struct {
				AgentNickname string                 `json:"agentNickname"`
				JaraStatus    string                 `json:"jaraStatus"`
				JaraAgentID   string                 `json:"jaraAgentId"`
				Turns         []protocol.HistoryTurn `json:"turns"`
				Status        struct {
					Type string `json:"type"`
				} `json:"status"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(m.Result, &r); err != nil {
			agent.readError = "unreadable transcript: " + err.Error()
			agent.touch()
			return true
		}
		if r.Thread.AgentNickname != "" && strings.HasPrefix(agent.name, "agent ") {
			agent.name = r.Thread.AgentNickname
		}
		if r.Thread.JaraAgentID != "" {
			agent.agentID = r.Thread.JaraAgentID
		}
		u.applyAgentThreadStatus(agent, r.Thread.JaraStatus, r.Thread.Status.Type)
		agent.turns, agent.readError, agent.notes, agent.stale = r.Thread.Turns, "", nil, false
		agent.touch()
	case "turn/start":
		if m.Error != nil {
			agent.notes = append(agent.notes, "Could not send the message: "+chatText(m.Error.Message))
		} else {
			u.setAgentStatus(agent, "running")
		}
		agent.stale = true
		agent.touch()
	}
	return true
}

// expireChatAgentRequest drops a subagent request that got no reply.
func (u *chatUI) expireChatAgentRequest(id string, pending chatPending) bool {
	agentID, ok := strings.CutPrefix(pending.action, "agent:")
	if !ok {
		agentID, ok = strings.CutPrefix(pending.action, "agentstatus:")
	}
	if !ok {
		return false
	}
	delete(u.pending, id)
	if agent := u.agentByID[agentID]; agent != nil {
		agent.reading = false
		if pending.method == "turn/start" {
			agent.notes = append(agent.notes, "The message got no reply in time.")
			agent.touch()
		}
	}
	return true
}

// messageChatAgent sends text to a subagent. Codex takes it in the subagent's
// thread; a Claude Code subagent gets it through the main agent.
func (u *chatUI) messageChatAgent(agent *chatAgent, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if agent.claude() {
		return u.relayToClaudeAgent(agent, text)
	}
	if u.chatOffline() {
		agent.notes = append(agent.notes, "Not connected; send the message again once the session reconnects.")
		agent.touch()
		return nil
	}
	agent.notes = append(agent.notes, "You: "+text)
	agent.touch()
	params := map[string]any{"threadId": agent.id, "jaraSubagent": true,
		"input": []map[string]any{{"type": "text", "text": text}}}
	return u.requestWithAction("turn/start", params, "agent:"+agent.id)
}

func (u *chatUI) relayToClaudeAgent(agent *chatAgent, text string) error {
	target := strconv.Quote(agent.name)
	if agent.agentID != "" {
		target += " (agent ID " + agent.agentID + ")"
	}
	relay := fmt.Sprintf("Use the SendMessage tool to send this message from me to your subagent %s, then briefly report its answer:\n\n%s", target, text)
	agent.notes = append(agent.notes, "You (relayed by the main agent): "+text)
	agent.touch()
	if u.agentTurnActive() || u.chatOffline() || !u.threadReady {
		if err := u.queuePrompt(u.thread, relay); err != nil {
			agent.notes = append(agent.notes, "Could not queue the message: "+err.Error())
			return nil
		}
		agent.notes = append(agent.notes, "The main agent is busy; the message is queued for it.")
		return nil
	}
	return u.sendPrompt(relay)
}
