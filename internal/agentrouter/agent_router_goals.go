package agentrouter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// Goals for Claude Code conversations mirror Codex's thread goals: while a
// goal is active the router keeps starting turns until Claude reports the
// goal complete or blocked, the token budget runs out, or the user interrupts.
// A user message always takes priority over the next automatic continuation.
const (
	maxGoalContinuations = 50
	GoalContinueDelay    = 400 * time.Millisecond
	goalCompleteMarker   = "GOAL COMPLETE"
	goalBlockedMarker    = "GOAL BLOCKED"
)

func goalPreamble(goal *protocol.ThreadGoal) string {
	if goal == nil || goal.Status != "active" {
		return ""
	}
	return fmt.Sprintf("[Active goal: %s\nKeep working toward it. When it is fully achieved, end your reply with a line %q. "+
		"If you cannot continue without the user, end with %q.]\n\n", goal.Objective, goalCompleteMarker+": <one-sentence summary>", goalBlockedMarker+": <reason>")
}

func (r *agentRouter) handleClaudeGoal(id json.RawMessage, method string, params json.RawMessage, thread *Thread) {
	switch method {
	case "thread/goal/get":
		r.reply(id, map[string]any{"goal": thread.Goal})
	case "thread/goal/clear":
		cleared := thread.Goal != nil
		thread.Goal = nil
		thread.Save()
		r.reply(id, map[string]any{"cleared": cleared})
		if cleared {
			r.notify("thread/goal/cleared", map[string]any{"threadId": thread.ID})
		}
	case "thread/goal/set":
		var request struct {
			Objective   string          `json:"objective"`
			Status      string          `json:"status"`
			TokenBudget json.RawMessage `json:"tokenBudget"`
		}
		_ = json.Unmarshal(params, &request)
		if thread.Goal == nil {
			if strings.TrimSpace(request.Objective) == "" {
				r.fail(id, "No goal is set")
				return
			}
			thread.Goal = &protocol.ThreadGoal{ThreadID: thread.ID, Status: "active"}
		}
		if request.Objective != "" {
			thread.Goal.Objective = request.Objective
		}
		if request.Status != "" {
			thread.Goal.Status = request.Status
		}
		if len(request.TokenBudget) > 0 {
			var budget *int64
			_ = json.Unmarshal(request.TokenBudget, &budget)
			thread.Goal.TokenBudget = budget
		}
		thread.goalContinuations = 0
		thread.Save()
		r.reply(id, map[string]any{"goal": thread.Goal})
		r.notify("thread/goal/updated", map[string]any{"threadId": thread.ID, "goal": thread.Goal})
		if !thread.crunchGoal {
			r.scheduleGoalContinuation(thread)
		}
	}
}

// scheduleGoalContinuation waits briefly so a queued user prompt, sent by the
// UI when the previous turn completed, can start first.
func (r *agentRouter) scheduleGoalContinuation(thread *Thread) {
	if thread.Goal == nil || thread.Goal.Status != "active" {
		return
	}
	events, id := r.events, thread.ID
	time.AfterFunc(GoalContinueDelay, func() { events <- routerEvent{source: "goal", thread: id} })
}

func (r *agentRouter) continueGoal(threadID string) {
	thread := r.threads[threadID]
	if thread == nil || thread.Goal == nil || thread.Goal.Status != "active" || thread.Current().Agent != protocol.AgentClaude {
		return
	}
	if thread.claude == nil {
		thread.claude = newClaudeSession(r, thread, thread.Current().BackendID, thread.Current().Started)
	}
	if thread.claude.turnID != "" {
		return
	}
	if thread.goalContinuations >= maxGoalContinuations {
		r.updateGoalStatus(thread, "blocked")
		return
	}
	thread.goalContinuations++
	thread.beginTurn("(continuing toward the goal)", protocol.AgentClaude)
	thread.claude.goalTurn = true
	_ = thread.claude.startTurn(nil, goalPreamble(thread.Goal)+"Continue working toward the goal.")
}

func (r *agentRouter) updateGoalStatus(thread *Thread, status string) {
	if thread.Goal == nil {
		return
	}
	thread.Goal.Status = status
	thread.Save()
	r.notify("thread/goal/updated", map[string]any{"threadId": thread.ID, "goal": thread.Goal})
}

// afterClaudeTurn accounts goal usage, applies the turn's outcome, then starts
// a queued user turn or the next goal continuation.
func (r *agentRouter) afterClaudeTurn(s *claudeSession, status, reply string, tokens int64, elapsed time.Duration) {
	thread := s.thread
	if goal := thread.Goal; goal != nil && goal.Status == "active" {
		goal.TokensUsed += tokens
		goal.TimeUsedSeconds += int64(elapsed.Seconds())
		next := "active"
		switch {
		case s.usageLimited:
			next = "usageLimited"
		case strings.Contains(reply, goalCompleteMarker):
			next = "complete"
		case strings.Contains(reply, goalBlockedMarker) || status == "failed":
			next = "blocked"
		case status == "interrupted":
			next = "paused"
		case goal.TokenBudget != nil && goal.TokensUsed >= *goal.TokenBudget:
			next = "budgetLimited"
		}
		r.updateGoalStatus(thread, next)
	}
	if len(s.queued) > 0 {
		next := s.queued[0]
		s.queued = s.queued[1:]
		_ = s.startTurn(next.id, next.message)
		return
	}
	// Crunch drives its own turns; continuing here would run them twice.
	if status == "completed" && !s.crunchTurn && !thread.crunchGoal {
		r.scheduleGoalContinuation(thread)
	}
}

// noteGoalDriver records that /goalcrunch, not the router, drives this goal,
// and removes the Jara-only marker before the request reaches Codex.
func (r *agentRouter) noteGoalDriver(params json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil {
		return params
	}
	var threadID, driver, objective string
	_ = json.Unmarshal(fields["threadId"], &threadID)
	_ = json.Unmarshal(fields["jaraDriver"], &driver)
	_ = json.Unmarshal(fields["objective"], &objective)
	// A new objective decides who drives; pause and resume keep the driver.
	if thread := r.threads[threadID]; thread != nil && objective != "" {
		thread.crunchGoal = driver == "crunch"
	}
	if _, ok := fields["jaraDriver"]; !ok {
		return params
	}
	delete(fields, "jaraDriver")
	out, _ := json.Marshal(fields)
	return out
}
