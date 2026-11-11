package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A routed thread is one UI conversation. Each segment is a stretch run by one
// agent; a model switch to another agent starts a new segment.
type threadSegment struct {
	Agent     string `json:"agent"`
	BackendID string `json:"backend_id"`
	Started   bool   `json:"started,omitempty"` // a Claude session exists to resume
}

type transcriptTurn struct {
	User    string           `json:"user"`
	Agent   string           `json:"agent,omitempty"`
	Reply   string           `json:"reply,omitempty"`
	Changes []chatFileChange `json:"changes,omitempty"`
	Actions []string         `json:"actions,omitempty"` // commands and plans, summarized
	At      int64            `json:"at"`
}

type routedThread struct {
	ID      string          `json:"id"`
	CWD     string          `json:"cwd"`
	Created int64           `json:"created"`
	Updated int64           `json:"updated"`
	Model   string          `json:"model,omitempty"` // Claude model ID
	Effort  string          `json:"effort,omitempty"`
	Goal    *chatThreadGoal `json:"goal,omitempty"`
	// Reviewer is the app-server approvalsReviewer the UI last selected.
	Reviewer string           `json:"approvalsReviewer,omitempty"`
	Segments []threadSegment  `json:"segments"`
	Turns    []transcriptTurn `json:"turns"`
	claude   *claudeSession

	goalContinuations int
	crunchGoal        bool // /goalcrunch drives the goal's turns
}

const maxTranscriptTurns = 200

var routedThreadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (t *routedThread) current() threadSegment { return t.Segments[len(t.Segments)-1] }

func (t *routedThread) persistent() bool {
	for _, segment := range t.Segments {
		if segment.Agent == agentClaude {
			return true
		}
	}
	return false
}

func agentThreadsDir() string { return filepath.Join(dataRoot(), "agent-threads") }

func (t *routedThread) save() {
	t.Updated = time.Now().Unix()
	if !t.persistent() || !routedThreadIDRE.MatchString(t.ID) {
		return
	}
	if len(t.Turns) > maxTranscriptTurns {
		t.Turns = t.Turns[len(t.Turns)-maxTranscriptTurns:]
	}
	if err := writeJSON(filepath.Join(agentThreadsDir(), t.ID+".json"), t); err != nil {
		fmt.Fprintln(os.Stderr, "save conversation record:", err)
	}
}

func loadRoutedThread(id string) *routedThread {
	if !routedThreadIDRE.MatchString(id) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(agentThreadsDir(), id+".json"))
	if err != nil {
		return nil
	}
	var thread routedThread
	if json.Unmarshal(data, &thread) != nil || thread.ID != id || len(thread.Segments) == 0 {
		return nil
	}
	return &thread
}

func listRoutedThreads() []routedThread {
	entries, _ := os.ReadDir(agentThreadsDir())
	var threads []routedThread
	for _, entry := range entries {
		if id, ok := strings.CutSuffix(entry.Name(), ".json"); ok {
			if thread := loadRoutedThread(id); thread != nil {
				threads = append(threads, *thread)
			}
		}
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].Updated > threads[j].Updated })
	return threads
}

func (t *routedThread) preview() string {
	for _, turn := range t.Turns {
		if strings.TrimSpace(turn.User) != "" {
			return fitMenu(strings.Join(strings.Fields(turn.User), " "), 120)
		}
	}
	return "Claude Code conversation"
}

func (t *routedThread) beginTurn(user, agent string) {
	t.Turns = append(t.Turns, transcriptTurn{User: user, Agent: agent, At: time.Now().Unix()})
}

func (t *routedThread) lastTurn() *transcriptTurn {
	if len(t.Turns) == 0 {
		return nil
	}
	return &t.Turns[len(t.Turns)-1]
}

func (r *agentRouter) startThread(id json.RawMessage, params json.RawMessage) error {
	if r.defaultAgent != agentClaude {
		return r.callCodex(codexCall{uiID: id, method: "thread/start"}, params)
	}
	var request struct {
		CWD string `json:"cwd"`
	}
	_ = json.Unmarshal(params, &request)
	threadID, err := newUUID()
	if err != nil {
		return err
	}
	thread := &routedThread{ID: threadID, CWD: request.CWD, Created: time.Now().Unix(), Model: claudeDefaultModel,
		Reviewer: requestedReviewer(params), Segments: []threadSegment{{Agent: agentClaude, BackendID: threadID}}}
	r.threads[threadID] = thread
	thread.claude = newClaudeSession(r, thread, threadID, false)
	r.reply(id, map[string]any{"thread": map[string]any{"id": threadID, "turns": []any{}}, "model": thread.Model})
	return nil
}

func (r *agentRouter) resumeThread(id json.RawMessage, params json.RawMessage) error {
	var request struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &request)
	thread := r.threads[request.ThreadID]
	if thread == nil {
		thread = loadRoutedThread(request.ThreadID)
		if thread != nil {
			r.threads[thread.ID] = thread
		}
	}
	if thread == nil {
		return r.callCodex(codexCall{uiID: id, method: "thread/resume"}, params)
	}
	thread.selectReviewer(params)
	segment := thread.current()
	if segment.Agent == agentClaude {
		if thread.claude == nil {
			thread.claude = newClaudeSession(r, thread, segment.BackendID, segment.Started)
		}
		thread.claude.syncPermissionMode()
		var effort any
		if thread.Effort != "" {
			effort = thread.Effort
		}
		r.reply(id, map[string]any{"thread": map[string]any{"id": thread.ID, "turns": thread.historyTurns()}, "model": thread.Model, "reasoningEffort": effort,
			"resolvedModel": thread.claude.resolvedModel})
		return nil
	}
	r.codexToVirtual[segment.BackendID] = thread.ID
	codexRequest, _ := json.Marshal(thread.codexOpenParams(map[string]any{"threadId": segment.BackendID}))
	return r.callCodex(codexCall{method: "thread/resume", callback: func(result, failure json.RawMessage) {
		if failure != nil {
			r.send(rpcMessage{ID: id, Error: failure})
			return
		}
		var response map[string]any
		_ = json.Unmarshal(result, &response)
		if response == nil {
			response = map[string]any{}
		}
		response["thread"] = map[string]any{"id": thread.ID, "turns": thread.historyTurns()}
		r.reply(id, response)
	}}, codexRequest)
}

// startTurn routes a turn to the thread's agent, switching agents first when
// the selected model belongs to the other one.
func (r *agentRouter) startTurn(id json.RawMessage, params json.RawMessage) error {
	var request struct {
		ThreadID string `json:"threadId"`
		Model    string `json:"model"`
		Effort   string `json:"effort"`
	}
	_ = json.Unmarshal(params, &request)
	thread := r.threads[request.ThreadID]
	if thread == nil {
		// A Codex-only conversation not seen before: start tracking it.
		thread = &routedThread{ID: request.ThreadID, Created: time.Now().Unix(), Segments: []threadSegment{{Agent: agentCodex, BackendID: request.ThreadID}}}
		r.threads[request.ThreadID] = thread
	}
	thread.selectReviewer(params)
	text := turnText(params)
	target := thread.current().Agent
	if request.Model != "" {
		target = agentForModel(request.Model)
	}
	from := thread.current().Agent
	switching := target != from
	message := text
	if switching {
		message = thread.switchBriefing(from, target, text)
	}
	thread.beginTurn(text, target)
	thread.goalContinuations = 0
	if target == agentClaude {
		message = goalPreamble(thread.Goal) + message
	}
	if target == agentClaude {
		if switching {
			backend, err := newUUID()
			if err != nil {
				return err
			}
			thread.Segments = append(thread.Segments, threadSegment{Agent: agentClaude, BackendID: backend})
			if thread.claude != nil {
				thread.claude.stop()
			}
			thread.claude = newClaudeSession(r, thread, backend, false)
		} else if thread.claude == nil {
			thread.claude = newClaudeSession(r, thread, thread.current().BackendID, thread.current().Started)
		}
		if request.Model != "" {
			thread.Model = request.Model
		}
		if request.Effort != "" {
			thread.Effort = request.Effort
		}
		thread.save()
		thread.claude.syncPermissionMode()
		return thread.claude.startTurn(id, message)
	}
	if !switching {
		return r.callCodex(codexCall{uiID: id, method: "turn/start"}, r.codexParams(params))
	}
	if thread.claude != nil {
		thread.claude.stop()
		thread.claude = nil
	}
	// Open a Codex thread for the rest of the conversation, then send the
	// briefing as its first turn.
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
		thread.Segments = append(thread.Segments, threadSegment{Agent: agentCodex, BackendID: started.Thread.ID})
		r.codexToVirtual[started.Thread.ID] = thread.ID
		thread.save()
		if goal := thread.Goal; goal != nil {
			// Carry the goal to Codex, which then pursues it natively.
			goalParams, _ := json.Marshal(map[string]any{"threadId": started.Thread.ID, "origin": "user", "objective": goal.Objective, "status": goal.Status, "tokenBudget": goal.TokenBudget})
			_ = r.callCodex(codexCall{method: "thread/goal/set", callback: func(json.RawMessage, json.RawMessage) {}}, goalParams)
		}
		var fields map[string]any
		_ = json.Unmarshal(params, &fields)
		fields["threadId"] = started.Thread.ID
		fields["input"] = []any{map[string]any{"type": "text", "text": message}}
		turnParams, _ := json.Marshal(fields)
		_ = r.callCodex(codexCall{uiID: id, method: "turn/start"}, turnParams)
	}}, startParams)
}

// postProcessCodexResult merges Claude entries into model and thread lists
// and starts tracking new Codex threads.
func (r *agentRouter) postProcessCodexResult(call codexCall, result, failure json.RawMessage) json.RawMessage {
	if failure != nil {
		return result
	}
	switch call.method {
	case "model/list":
		return mergeClaudeModels(result)
	case "thread/list":
		return r.mergeThreadList(result)
	case "thread/start", "thread/resume":
		var response struct {
			Thread struct {
				ID    string            `json:"id"`
				CWD   string            `json:"cwd"`
				Turns []chatHistoryTurn `json:"turns"`
			} `json:"thread"`
		}
		if json.Unmarshal(result, &response) == nil && response.Thread.ID != "" && r.threads[response.Thread.ID] == nil {
			thread := &routedThread{ID: response.Thread.ID, CWD: response.Thread.CWD, Created: time.Now().Unix(),
				Segments: []threadSegment{{Agent: agentCodex, BackendID: response.Thread.ID}}}
			thread.seedFromHistory(response.Thread.Turns)
			r.threads[thread.ID] = thread
		}
	}
	return result
}

func (r *agentRouter) mergeThreadList(result json.RawMessage) json.RawMessage {
	var page map[string]json.RawMessage
	if json.Unmarshal(result, &page) != nil {
		return result
	}
	var data []map[string]any
	if json.Unmarshal(page["data"], &data) != nil {
		return result
	}
	hidden := map[string]bool{}
	var extra []map[string]any
	for _, thread := range listRoutedThreads() {
		for _, segment := range thread.Segments {
			hidden[segment.BackendID] = true // listed once, as the routed conversation
		}
		extra = append(extra, map[string]any{"id": thread.ID, "name": "", "preview": thread.preview(), "cwd": thread.CWD,
			"updatedAt": thread.Updated, "createdAt": thread.Created, "status": map[string]string{"type": "notLoaded"}})
	}
	merged := extra
	for _, entry := range data {
		if id, _ := entry["id"].(string); !hidden[id] {
			merged = append(merged, entry)
		}
	}
	page["data"], _ = json.Marshal(merged)
	out, err := json.Marshal(page)
	if err != nil {
		return result
	}
	return out
}

// requestedReviewer reads the approvalsReviewer a UI request selects, if any.
func requestedReviewer(params json.RawMessage) string {
	var request struct {
		Reviewer string `json:"approvalsReviewer"`
	}
	_ = json.Unmarshal(params, &request)
	return request.Reviewer
}

// selectReviewer records a newly requested approvals reviewer; requests that
// name none keep the thread's current choice.
func (t *routedThread) selectReviewer(params json.RawMessage) {
	if reviewer := requestedReviewer(params); reviewer != "" && reviewer != t.Reviewer {
		t.Reviewer = reviewer
		t.save()
	}
}

// autoApprovals reports whether approvals go to an automatic reviewer.
func (t *routedThread) autoApprovals() bool {
	return t.Reviewer == chatAutoReviewer || t.Reviewer == "guardian_subagent"
}

// codexOpenParams completes thread/start or thread/resume parameters for a
// Codex segment so it keeps the conversation's approvals reviewer.
func (t *routedThread) codexOpenParams(params map[string]any) map[string]any {
	params["cwd"] = t.CWD
	if t.Reviewer != "" {
		params["approvalsReviewer"] = t.Reviewer
	}
	return params
}
