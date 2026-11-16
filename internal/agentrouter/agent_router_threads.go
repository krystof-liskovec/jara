package agentrouter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// A routed thread is one UI conversation. Each segment is a stretch run by one
// agent; a model switch to another agent starts a new segment.
type ThreadSegment struct {
	Agent     string `json:"agent"`
	BackendID string `json:"backend_id"`
	Started   bool   `json:"started,omitempty"` // a Claude session exists to resume
	// StartTurn is the 1-based transcript index of the segment's first turn;
	// 0 in conversations saved before Jara recorded it.
	StartTurn int `json:"start_turn,omitempty"`
}

type TranscriptTurn struct {
	ID      string                `json:"id,omitempty"` // Codex turn ID, for /rewind
	User    string                `json:"user"`
	Agent   string                `json:"agent,omitempty"`
	Reply   string                `json:"reply,omitempty"`
	Changes []protocol.FileChange `json:"changes,omitempty"`
	Actions []string              `json:"actions,omitempty"` // commands and plans, summarized
	At      int64                 `json:"at"`
}

type Thread struct {
	ID      string               `json:"id"`
	CWD     string               `json:"cwd"`
	Created int64                `json:"created"`
	Updated int64                `json:"updated"`
	Model   string               `json:"model,omitempty"` // Claude model ID
	Effort  string               `json:"effort,omitempty"`
	Goal    *protocol.ThreadGoal `json:"goal,omitempty"`
	// Reviewer is the app-server approvalsReviewer the UI last selected.
	Reviewer string           `json:"approvalsReviewer,omitempty"`
	Segments []ThreadSegment  `json:"segments"`
	Turns    []TranscriptTurn `json:"turns"`
	claude   *claudeSession

	// Rebrief starts the next turn in a fresh agent session briefed on the
	// transcript, after /rewind dropped turns that session still remembers.
	Rebrief bool `json:"rebrief,omitempty"`
	// RebriefMoved explains the rebrief as a move to another machine.
	RebriefMoved bool `json:"rebrief_moved,omitempty"`
	// Portable conversations can move between machines, so Jara keeps their
	// transcript even when only Codex ran them.
	Portable bool `json:"portable,omitempty"`

	goalContinuations int
	crunchGoal        bool // /goalcrunch drives the goal's turns
}

const maxTranscriptTurns = 200

var routedThreadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (t *Thread) Current() ThreadSegment { return t.Segments[len(t.Segments)-1] }

func (t *Thread) persistent() bool {
	if t.Portable || len(t.Segments) > 1 || t.Rebrief {
		return true
	}
	for _, segment := range t.Segments {
		if segment.Agent == protocol.AgentClaude {
			return true
		}
	}
	return false
}

func agentThreadsDir() string { return filepath.Join(fsutil.DataRoot(), "agent-threads") }

func (t *Thread) Save() {
	t.Updated = time.Now().Unix()
	if !t.persistent() || !routedThreadIDRE.MatchString(t.ID) {
		return
	}
	if removed := len(t.Turns) - maxTranscriptTurns; removed > 0 {
		t.Turns = t.Turns[removed:]
		for i := range t.Segments {
			if t.Segments[i].StartTurn > 0 {
				t.Segments[i].StartTurn = max(1, t.Segments[i].StartTurn-removed)
			}
		}
	}
	if err := fsutil.WriteJSON(filepath.Join(agentThreadsDir(), t.ID+".json"), t); err != nil {
		fmt.Fprintln(os.Stderr, "save conversation record:", err)
	}
}

func LoadThread(id string) *Thread {
	if !routedThreadIDRE.MatchString(id) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(agentThreadsDir(), id+".json"))
	if err != nil {
		return nil
	}
	var thread Thread
	if json.Unmarshal(data, &thread) != nil || thread.ID != id || len(thread.Segments) == 0 {
		return nil
	}
	return &thread
}

func ListThreads() []Thread {
	entries, _ := os.ReadDir(agentThreadsDir())
	var threads []Thread
	for _, entry := range entries {
		if id, ok := strings.CutSuffix(entry.Name(), ".json"); ok {
			if thread := LoadThread(id); thread != nil {
				threads = append(threads, *thread)
			}
		}
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].Updated > threads[j].Updated })
	return threads
}

func (t *Thread) Preview() string {
	for _, turn := range t.Turns {
		if strings.TrimSpace(turn.User) != "" {
			return textutil.FitText(strings.Join(strings.Fields(turn.User), " "), 120)
		}
	}
	return "Claude Code conversation"
}

func (t *Thread) beginTurn(user, agent string) {
	t.Turns = append(t.Turns, TranscriptTurn{User: user, Agent: agent, At: time.Now().Unix()})
}

func (t *Thread) lastTurn() *TranscriptTurn {
	if len(t.Turns) == 0 {
		return nil
	}
	return &t.Turns[len(t.Turns)-1]
}

func (r *agentRouter) startThread(id json.RawMessage, params json.RawMessage) error {
	agent, model := r.startAgent()
	if agent != protocol.AgentClaude {
		if model != "" {
			params = withJSONField(params, "model", model)
		}
		return r.callCodex(codexCall{uiID: id, method: "thread/start"}, params)
	}
	var request struct {
		CWD string `json:"cwd"`
	}
	_ = json.Unmarshal(params, &request)
	threadID, err := protocol.NewUUID()
	if err != nil {
		return err
	}
	thread := &Thread{ID: threadID, CWD: request.CWD, Created: time.Now().Unix(), Model: textutil.FirstNonEmpty(model, protocol.ClaudeDefaultModel),
		Reviewer: requestedReviewer(params), Segments: []ThreadSegment{{Agent: protocol.AgentClaude, BackendID: threadID, StartTurn: 1}},
		Portable: portableThreads()}
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
		thread = LoadThread(request.ThreadID)
		if thread != nil {
			r.threads[thread.ID] = thread
		}
	}
	if thread == nil {
		return r.callCodex(codexCall{uiID: id, method: "thread/resume"}, params)
	}
	thread.selectReviewer(params)
	segment := thread.Current()
	if segment.Agent == protocol.AgentClaude {
		if thread.claude == nil {
			thread.claude = newClaudeSession(r, thread, segment.BackendID, segment.Started)
		}
		thread.claude.syncPermissionMode()
		var effort any
		if thread.Effort != "" {
			effort = thread.Effort
		}
		r.reply(id, map[string]any{"thread": map[string]any{"id": thread.ID, "turns": thread.HistoryTurns()}, "model": thread.Model, "reasoningEffort": effort,
			"resolvedModel": thread.claude.resolvedModel})
		return nil
	}
	r.codexToVirtual[segment.BackendID] = thread.ID
	codexRequest, _ := json.Marshal(thread.codexOpenParams(map[string]any{"threadId": segment.BackendID}))
	return r.callCodex(codexCall{method: "thread/resume", callback: func(result, failure json.RawMessage) {
		if failure != nil && !thread.rebriefAfterMove() {
			r.send(rpcMessage{ID: id, Error: failure})
			return
		}
		var response map[string]any
		_ = json.Unmarshal(result, &response)
		if response == nil {
			response = map[string]any{}
		}
		response["thread"] = map[string]any{"id": thread.ID, "turns": thread.HistoryTurns()}
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
		thread = &Thread{ID: request.ThreadID, Created: time.Now().Unix(), Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: request.ThreadID, StartTurn: 1}},
			Portable: portableThreads()}
		r.threads[request.ThreadID] = thread
	}
	thread.selectReviewer(params)
	text := turnText(params)
	target := thread.Current().Agent
	if request.Model != "" {
		target = protocol.AgentForModel(request.Model)
	}
	from := thread.Current().Agent
	switching := target != from
	message := text
	var plan agentSwitch
	if switching {
		plan = thread.planSwitch(from, target, text)
		message = textutil.FirstNonEmpty(plan.returning, plan.fresh)
	}
	thread.checkMovedClaudeSession()
	if thread.Rebrief {
		header := rewindBriefing
		if thread.RebriefMoved {
			header = movedBriefing
		}
		thread.Rebrief, thread.RebriefMoved, switching = false, false, true
		plan = agentSwitch{from: from, to: target, previous: -1, fresh: thread.briefing(header, thread.Turns, text)}
		message = plan.fresh
	}
	startTurn := len(thread.Turns) + 1
	thread.beginTurn(text, target)
	thread.goalContinuations = 0
	if target == protocol.AgentClaude {
		message = goalPreamble(thread.Goal) + message
		if switching {
			if thread.claude != nil {
				thread.claude.stop()
			}
			segment, err := thread.claudeSegment(plan, startTurn)
			if err != nil {
				return err
			}
			thread.Segments = append(thread.Segments, segment)
			thread.claude = newClaudeSession(r, thread, segment.BackendID, segment.Started)
		} else if thread.claude == nil {
			thread.claude = newClaudeSession(r, thread, thread.Current().BackendID, thread.Current().Started)
		}
		if request.Model != "" {
			thread.Model = request.Model
		}
		if request.Effort != "" {
			thread.Effort = request.Effort
		}
		thread.Save()
		thread.claude.syncPermissionMode()
		thread.claude.images = turnImages(params)
		return thread.claude.startTurn(id, message)
	}
	if !switching {
		return r.callCodex(codexCall{uiID: id, method: "turn/start"}, r.codexParams(params))
	}
	if thread.claude != nil {
		thread.claude.stop()
		thread.claude = nil
	}
	return r.switchToCodex(id, thread, params, plan, startTurn)
}

// postProcessCodexResult merges Claude entries into model and thread lists
// and starts tracking new Codex threads.
func (r *agentRouter) postProcessCodexResult(call codexCall, result, failure json.RawMessage) json.RawMessage {
	if failure != nil {
		return result
	}
	switch call.method {
	case "model/list":
		return protocol.MergeClaudeModels(result)
	case "thread/list":
		return r.mergeThreadList(result)
	case "thread/start", "thread/resume":
		var response struct {
			Thread struct {
				ID    string                 `json:"id"`
				CWD   string                 `json:"cwd"`
				Turns []protocol.HistoryTurn `json:"turns"`
			} `json:"thread"`
		}
		if json.Unmarshal(result, &response) == nil && response.Thread.ID != "" && r.threads[response.Thread.ID] == nil {
			thread := &Thread{ID: response.Thread.ID, CWD: response.Thread.CWD, Created: time.Now().Unix(),
				Segments: []ThreadSegment{{Agent: protocol.AgentCodex, BackendID: response.Thread.ID, StartTurn: 1}}, Portable: portableThreads()}
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
	for _, thread := range ListThreads() {
		for _, segment := range thread.Segments {
			hidden[segment.BackendID] = true // listed once, as the routed conversation
		}
		extra = append(extra, map[string]any{"id": thread.ID, "name": "", "preview": thread.Preview(), "cwd": thread.CWD,
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
func (t *Thread) selectReviewer(params json.RawMessage) {
	if reviewer := requestedReviewer(params); reviewer != "" && reviewer != t.Reviewer {
		t.Reviewer = reviewer
		t.Save()
	}
}

// autoApprovals reports whether approvals go to an automatic reviewer.
func (t *Thread) autoApprovals() bool {
	return t.Reviewer == protocol.AutoReviewer || t.Reviewer == "guardian_subagent"
}

// codexOpenParams completes thread/start or thread/resume parameters for a
// Codex segment so it keeps the conversation's approvals reviewer.
func (t *Thread) codexOpenParams(params map[string]any) map[string]any {
	params["cwd"] = t.CWD
	if t.Reviewer != "" {
		params["approvalsReviewer"] = t.Reviewer
	}
	return params
}

// ResumeCommand reopens the latest segment in its agent's own terminal UI.
// Claude Code finds sessions by working directory, so callers start it in
// the conversation's directory.
func (t *Thread) ResumeCommand() []string {
	segment := t.Current()
	if segment.Agent == protocol.AgentClaude {
		return []string{protocol.ClaudeBinary, "--resume", segment.BackendID}
	}
	command := []string{protocol.CodexBinary, "resume", segment.BackendID, "--include-non-interactive"}
	if t.CWD != "" {
		command = append(command, "-C", t.CWD)
	}
	return command
}
