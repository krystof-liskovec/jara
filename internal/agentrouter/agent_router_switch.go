package agentrouter

import (
	"encoding/json"
	"fmt"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// Switching agents keeps one conversation across providers. Agent histories
// are not interchangeable, so the receiving agent is briefed in text; the
// working tree already holds every change. An agent that worked on the
// conversation earlier resumes its own session and is briefed only on the
// turns it missed, keeping the full context it built up.

const switchBriefingLimit = 40000

type agentSwitch struct {
	from, to  string
	previous  int    // the target agent's latest earlier segment, or -1
	fresh     string // briefing for a new session: the whole exchange
	returning string // briefing for resuming previous: the missed turns
}

func (t *Thread) planSwitch(from, to, message string) agentSwitch {
	plan := agentSwitch{from: from, to: to, previous: -1}
	plan.fresh = t.briefing(fmt.Sprintf("[Jara: this conversation started with %s. The user switched it to you (%s). "+
		"The earlier exchange is replayed below; files in the working tree already include every change made so far. "+
		"Continue from here.]\n\n", protocol.AgentDisplayName(from), protocol.AgentDisplayName(to)), t.Turns, message)
	for i := len(t.Segments) - 2; i >= 0; i-- {
		segment := t.Segments[i]
		if segment.Agent != to {
			continue
		}
		next := t.Segments[i+1].StartTurn
		resumable := segment.BackendID != "" && (to != protocol.AgentClaude || segment.Started)
		if resumable && next > 0 && next <= len(t.Turns)+1 {
			plan.previous = i
			plan.returning = t.briefing(fmt.Sprintf("[Jara: you are back in this conversation. Since your last reply the user "+
				"continued with %s; those turns are below and the working tree already includes their changes. "+
				"Continue from here.]\n\n", protocol.AgentDisplayName(from)), t.Turns[next-1:], message)
		}
		break
	}
	return plan
}

func (t *Thread) briefing(header string, turns []TranscriptTurn, message string) string {
	var b strings.Builder
	b.WriteString(header)
	var replay []string
	for _, turn := range turns {
		entry := "User: " + turn.User
		if turn.Reply != "" {
			entry += "\n" + protocol.AgentDisplayName(turn.Agent) + ": " + turn.Reply
		}
		for _, action := range turn.Actions {
			entry += "\n(" + protocol.AgentDisplayName(turn.Agent) + " " + action + ")"
		}
		for _, change := range turn.Changes {
			entry += "\n(" + protocol.AgentDisplayName(turn.Agent) + " edited " + textutil.FirstNonEmpty(change.Path, change.NewPath, change.OldPath) + ")"
		}
		replay = append(replay, entry)
	}
	if t.Goal != nil && t.Goal.Status == "active" {
		fmt.Fprintf(&b, "[Active goal: %s]\n\n", t.Goal.Objective)
	}
	text := strings.Join(replay, "\n\n")
	if len(text) > switchBriefingLimit {
		text = "… (earlier turns omitted)\n" + text[len(text)-switchBriefingLimit:]
	}
	b.WriteString(text)
	b.WriteString("\n\n[New message from the user]\n" + message)
	return b.String()
}

// claudeSegment resumes Claude Code's earlier session when there is one and
// otherwise starts a new session.
func (t *Thread) claudeSegment(plan agentSwitch, startTurn int) (ThreadSegment, error) {
	if plan.previous >= 0 {
		return ThreadSegment{Agent: protocol.AgentClaude, BackendID: t.Segments[plan.previous].BackendID, Started: true, StartTurn: startTurn}, nil
	}
	backend, err := protocol.NewUUID()
	return ThreadSegment{Agent: protocol.AgentClaude, BackendID: backend, StartTurn: startTurn}, err
}

// switchToCodex resumes Codex's earlier thread when there is one, falling
// back to a new thread with the full briefing if it cannot be resumed.
func (r *agentRouter) switchToCodex(id json.RawMessage, thread *Thread, params json.RawMessage, plan agentSwitch, startTurn int) error {
	if plan.previous < 0 {
		return r.openCodexSegment(id, thread, params, plan.fresh, startTurn)
	}
	backend := thread.Segments[plan.previous].BackendID
	resumeParams, _ := json.Marshal(thread.codexOpenParams(map[string]any{"threadId": backend}))
	return r.callCodex(codexCall{method: "thread/resume", callback: func(_, failure json.RawMessage) {
		if failure != nil {
			_ = r.openCodexSegment(id, thread, params, plan.fresh, startTurn)
			return
		}
		r.continueOnCodex(id, thread, params, backend, plan.returning, startTurn)
	}}, resumeParams)
}

func (r *agentRouter) openCodexSegment(id json.RawMessage, thread *Thread, params json.RawMessage, message string, startTurn int) error {
	startParams, _ := json.Marshal(thread.codexOpenParams(map[string]any{}))
	return r.callCodex(codexCall{method: "thread/start", callback: func(result, failure json.RawMessage) {
		var started struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if failure != nil || json.Unmarshal(result, &started) != nil || started.Thread.ID == "" {
			r.fail(id, "could not open a Codex conversation for the switch")
			return
		}
		r.continueOnCodex(id, thread, params, started.Thread.ID, message, startTurn)
	}}, startParams)
}

// continueOnCodex records the Codex segment, carries the goal over, and sends
// the briefing as the turn.
func (r *agentRouter) continueOnCodex(id json.RawMessage, thread *Thread, params json.RawMessage, backend, message string, startTurn int) {
	thread.Segments = append(thread.Segments, ThreadSegment{Agent: protocol.AgentCodex, BackendID: backend, StartTurn: startTurn})
	r.codexToVirtual[backend] = thread.ID
	thread.Save()
	if goal := thread.Goal; goal != nil {
		goalParams, _ := json.Marshal(map[string]any{"threadId": backend, "origin": "user", "objective": goal.Objective, "status": goal.Status, "tokenBudget": goal.TokenBudget})
		_ = r.callCodex(codexCall{method: "thread/goal/set", callback: func(json.RawMessage, json.RawMessage) {}}, goalParams)
	}
	var fields map[string]any
	_ = json.Unmarshal(params, &fields)
	fields["threadId"] = backend
	fields["input"] = append([]any{map[string]any{"type": "text", "text": message}}, turnAttachments(params)...)
	turnParams, _ := json.Marshal(fields)
	_ = r.callCodex(codexCall{uiID: id, method: "turn/start"}, turnParams)
}
