package agentrouter

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// A Codex main agent can end its turn while its subagents still run. When
// one of them finishes while the main agent is idle, the router waits
// briefly for Codex to continue by itself, then starts a main-agent turn
// telling it so. Completion is seen from the subagent's own notifications,
// from the parent's subagent items, or, when neither arrives, by checking
// the running subagents' threads while the main agent is idle.

// SubagentCheckInterval is how often the router checks running Codex
// subagents of an idle main agent.
var SubagentCheckInterval = 5 * time.Second

type codexChild struct {
	parent, name string
	running      bool
	checking     bool
	byUser       bool // running a message you sent it, not work for the main agent
}

type codexBackground struct {
	children map[string]*codexChild // Codex subagent thread -> its state
	busy     map[string]bool        // main threads (UI IDs) with a turn running
	finished map[string][]string    // main thread -> subagents that finished while it was idle
	polling  map[string]bool        // main threads with a scheduled check
}

func (r *agentRouter) background() *codexBackground {
	if r.codexBackground == nil {
		r.codexBackground = &codexBackground{children: map[string]*codexChild{}, busy: map[string]bool{},
			finished: map[string][]string{}, polling: map[string]bool{}}
	}
	return r.codexBackground
}

// observeCodexBackground follows main-agent turns and subagent lifecycles.
func (r *agentRouter) observeCodexBackground(m rpcMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Status   struct {
			Type string `json:"type"`
		} `json:"status"`
		Item protocol.HistoryItem `json:"item"`
	}
	if json.Unmarshal(m.Params, &p) != nil || p.ThreadID == "" {
		return
	}
	b := r.background()
	if child := b.children[p.ThreadID]; child != nil {
		switch {
		case m.Method == "turn/started" || m.Method == "thread/status/changed" && p.Status.Type == "active":
			child.running = true
		case m.Method == "turn/completed" || m.Method == "thread/status/changed" && p.Status.Type != "active":
			r.codexChildFinished(p.ThreadID)
		}
		return
	}
	thread := r.threads[p.ThreadID]
	if thread == nil || thread.Current().Agent != protocol.AgentCodex {
		return
	}
	switch m.Method {
	case "turn/started":
		b.busy[p.ThreadID] = true
		delete(b.finished, p.ThreadID) // this turn takes up what finished
	case "turn/completed":
		b.busy[p.ThreadID] = false
		r.scheduleCodexChildCheck(p.ThreadID)
	case "item/started", "item/completed":
		r.noteCodexChildItem(p.ThreadID, p.Item)
	}
}

func (r *agentRouter) noteCodexChildItem(parent string, item protocol.HistoryItem) {
	b := r.background()
	child := func(id string) *codexChild {
		c := b.children[id]
		if c == nil && id != "" {
			c = &codexChild{parent: parent, name: "subagent", running: true}
			b.children[id] = c
		}
		return c
	}
	switch item.Type {
	case "subAgentActivity":
		c := child(item.AgentThreadID)
		if c == nil {
			return
		}
		if item.AgentPath != "" {
			c.name = item.AgentPath
		}
		switch item.Kind {
		case "started", "interacted":
			c.running = true
		case "completed":
			r.codexChildFinished(item.AgentThreadID)
		case "interrupted":
			c.running = false
		}
	case "collabAgentToolCall":
		if item.Tool == "spawnAgent" || item.Tool == "spawn_agent" || item.Tool == "sendInput" || item.Tool == "followupTask" {
			for _, id := range item.ReceiverThreadIDs {
				child(id).running = true
			}
		}
		for id, state := range item.AgentsStates {
			if c := b.children[id]; c != nil && state.Status != "running" && state.Status != "pendingInit" {
				c.running = false // the main agent saw it finish
			}
		}
	}
}

// codexChildFinished notes a running subagent that finished; an idle main
// agent is woken shortly unless Codex continues by itself.
func (r *agentRouter) codexChildFinished(id string) {
	b := r.background()
	child := b.children[id]
	if child == nil || !child.running {
		return
	}
	child.running = false
	if child.byUser {
		child.byUser = false // its answer is for you
		return
	}
	if b.busy[child.parent] {
		return // the running turn sees it
	}
	b.finished[child.parent] = append(b.finished[child.parent], child.name)
	events, parent := r.events, child.parent
	if events != nil {
		time.AfterFunc(SubagentWakeDelay, func() { events <- routerEvent{source: "codex-wake", thread: parent} })
	}
}

// wakeCodexParent starts a main-agent turn about finished subagents.
func (r *agentRouter) wakeCodexParent(parent string) {
	b := r.background()
	names := b.finished[parent]
	thread := r.threads[parent]
	if len(names) == 0 || b.busy[parent] || thread == nil || thread.Current().Agent != protocol.AgentCodex {
		return
	}
	delete(b.finished, parent)
	label := fmt.Sprintf("(subagent %q finished)", names[0])
	if len(names) > 1 {
		label = fmt.Sprintf("(%d subagents finished)", len(names))
	}
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = strconv.Quote(name)
	}
	prompt := fmt.Sprintf("Your subagent %s finished while you were idle. Read the results and continue the task; "+
		"if other subagents are still running, wait for them.", strings.Join(quoted, ", "))
	params, _ := json.Marshal(map[string]any{"threadId": parent, "input": []map[string]any{{"type": "text", "text": prompt}}})
	thread.beginTurn(label, protocol.AgentCodex)
	b.busy[parent] = true // until Codex reports the turn, or refuses it
	_ = r.callCodex(codexCall{method: "turn/start", callback: func(_ json.RawMessage, failure json.RawMessage) {
		if failure != nil {
			b.busy[parent] = false
		}
	}}, r.codexParams(params))
}

// scheduleCodexChildCheck checks an idle main agent's running subagents
// again after a while.
func (r *agentRouter) scheduleCodexChildCheck(parent string) {
	b := r.background()
	if b.polling[parent] || b.busy[parent] || !r.codexChildrenRunning(parent) || r.events == nil {
		return
	}
	b.polling[parent] = true
	events := r.events
	time.AfterFunc(SubagentCheckInterval, func() { events <- routerEvent{source: "codex-check", thread: parent} })
}

func (r *agentRouter) codexChildrenRunning(parent string) bool {
	for _, child := range r.background().children {
		if child.parent == parent && child.running {
			return true
		}
	}
	return false
}

// checkCodexChildren reads the threads of an idle main agent's running
// subagents; one no longer active has finished.
func (r *agentRouter) checkCodexChildren(parent string) {
	b := r.background()
	b.polling[parent] = false
	if b.busy[parent] {
		return
	}
	for id, child := range b.children {
		if child.parent != parent || !child.running || child.checking {
			continue
		}
		child.checking = true
		params, _ := json.Marshal(map[string]any{"threadId": id, "includeTurns": false})
		_ = r.callCodex(codexCall{method: "thread/read", callback: func(result json.RawMessage, failure json.RawMessage) {
			child.checking = false
			var read struct {
				Thread struct {
					Status struct {
						Type string `json:"type"`
					} `json:"status"`
				} `json:"thread"`
			}
			if failure == nil && json.Unmarshal(result, &read) == nil && read.Thread.Status.Type != "" && read.Thread.Status.Type != "active" {
				r.codexChildFinished(id)
			}
			r.scheduleCodexChildCheck(parent)
		}}, params)
	}
	r.scheduleCodexChildCheck(parent)
}

// noteUserSubagentTurn marks a subagent turn you started from the interface.
func (r *agentRouter) noteUserSubagentTurn(thread string) {
	if child := r.background().children[thread]; child != nil {
		child.running, child.byUser = true, true
	}
}
