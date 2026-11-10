package jara

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupFakeAgents(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JARA_FAKE_AGENT", "1")
	t.Setenv("JARA_FAKE_LOG", filepath.Join(dir, "log.jsonl"))
	t.Setenv("JARA_CLAUDE_BIN", "/proc/self/exe")
	t.Setenv("JARA_DATA", filepath.Join(dir, "data"))
	t.Setenv("JARA_CLAUDE_ARGS", "[]")
}

func agentMessages(seen []map[string]any) string {
	var b strings.Builder
	for _, m := range seen {
		params, _ := m["params"].(map[string]any)
		if m["method"] == "item/agentMessage/delta" {
			b.WriteString(params["delta"].(string))
		}
		if m["method"] == "item/completed" {
			if item, _ := params["item"].(map[string]any); item["type"] == "agentMessage" {
				b.WriteString("|" + item["text"].(string))
			}
		}
	}
	return b.String()
}

func threadIDs(seen []map[string]any) map[string]bool {
	ids := map[string]bool{}
	for _, m := range seen {
		if params, ok := m["params"].(map[string]any); ok {
			if id, ok := params["threadId"].(string); ok {
				ids[id] = true
			}
		}
	}
	return ids
}

func TestAgentRouterSwitchesBetweenCodexAndClaude(t *testing.T) {
	setupFakeAgents(t)
	cwd := t.TempDir()
	h := startRouterHarness(t, agentCodex)
	h.response(h.request("initialize", map[string]any{}))
	models := h.response(h.request("model/list", map[string]any{}))
	encoded, _ := json.Marshal(models)
	if !strings.Contains(string(encoded), `"gpt-x"`) || !strings.Contains(string(encoded), `"claude/opus"`) {
		t.Fatalf("model list must offer both agents: %s", encoded)
	}
	started := h.response(h.request("thread/start", map[string]any{"cwd": cwd}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	if thread != "codex-thread-1" {
		t.Fatalf("thread %q", thread)
	}

	h.request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "hello"}}})
	_, seen := h.waitFor(isMethod("turn/completed"))
	if !strings.Contains(agentMessages(seen), "codex: hello") {
		t.Fatalf("codex turn: %v", seen)
	}

	// Picking a Claude model moves the conversation to Claude Code.
	id := h.request("turn/start", map[string]any{"threadId": thread, "model": "claude/sonnet", "input": []any{map[string]any{"type": "text", "text": "continue please"}}})
	_, seen = h.waitFor(isMethod("turn/completed"))
	if !strings.Contains(agentMessages(seen), "claude: continue please") {
		t.Fatalf("claude turn: %v", seen)
	}
	if ids := threadIDs(seen); len(ids) != 1 || !ids[thread] {
		t.Fatalf("UI thread ID must stay stable: %v", ids)
	}
	if !hasResponse(seen, id) {
		t.Fatal("turn/start must be answered")
	}
	prompts := readFakeLog(t, "claude-prompt")
	if len(prompts) != 1 || !strings.Contains(prompts[0], "started with Codex") || !strings.Contains(prompts[0], "User: hello") || !strings.Contains(prompts[0], "Codex: codex: hello") {
		t.Fatalf("switch briefing: %q", prompts)
	}
	if args := readFakeLog(t, "claude-args"); len(args) != 1 || !strings.Contains(args[0], "--model sonnet") || !strings.Contains(args[0], "--session-id") {
		t.Fatalf("claude args: %q", args)
	}

	// A Claude edit asks for approval and reports a diff for the pane.
	h.request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "EDIT now"}}})
	approval, _ := h.waitFor(isMethod("item/fileChange/requestApproval"))
	h.send(map[string]any{"id": approval["id"], "result": map[string]any{"decision": "accept"}})
	_, seen = h.waitFor(isMethod("turn/completed"))
	if got := readFakeLog(t, "claude-permission"); len(got) != 1 || got[0] != "allow" {
		t.Fatalf("permission answer: %v", got)
	}
	diff := ""
	for _, m := range seen {
		if m["method"] == "item/completed" {
			item := m["params"].(map[string]any)["item"].(map[string]any)
			if item["type"] == "fileChange" {
				encoded, _ := json.Marshal(item["changes"])
				diff = string(encoded)
			}
		}
	}
	if !strings.Contains(diff, `-beta\n+BETA`) || !strings.Contains(diff, "/w/app.txt") {
		t.Fatalf("file change diff: %s", diff)
	}

	// Picking a Codex model moves it back, with the Claude part in the briefing.
	h.request("turn/start", map[string]any{"threadId": thread, "model": "gpt-x", "input": []any{map[string]any{"type": "text", "text": "back to codex"}}})
	_, seen = h.waitFor(isMethod("turn/completed"))
	if ids := threadIDs(seen); len(ids) != 1 || !ids[thread] {
		t.Fatalf("Codex notifications must use the UI thread ID: %v", ids)
	}
	turns := readFakeLog(t, "codex-turn")
	last := turns[len(turns)-1]
	if !strings.HasPrefix(last, "codex-thread-2|") || !strings.Contains(last, "started with Codex") && !strings.Contains(last, "switched it to you (Codex)") || !strings.Contains(last, "edited /w/app.txt") {
		t.Fatalf("codex briefing: %q", last)
	}

	// The mixed conversation is listed once and resumes with its history.
	list := h.response(h.request("thread/list", map[string]any{"cwd": cwd}))
	encoded, _ = json.Marshal(list)
	if strings.Count(string(encoded), `"id":"`+thread+`"`) != 1 || strings.Contains(string(encoded), "codex-thread-2") || !strings.Contains(string(encoded), "codex-other") {
		t.Fatalf("thread list: %s", encoded)
	}
	h2 := startRouterHarness(t, agentCodex)
	h2.response(h2.request("initialize", map[string]any{}))
	resumed := h2.response(h2.request("thread/resume", map[string]any{"threadId": thread}))
	history, _ := json.Marshal(resumed["result"].(map[string]any)["thread"])
	for _, want := range []string{"hello", "continue please", "EDIT now", "back to codex", "BETA"} {
		if !strings.Contains(string(history), want) {
			t.Fatalf("resumed history lacks %q: %s", want, history)
		}
	}
}

func hasResponse(seen []map[string]any, id float64) bool {
	for _, m := range seen {
		if v, ok := m["id"].(float64); ok && v == id && m["method"] == nil {
			return m["error"] == nil
		}
	}
	return false
}

func TestAgentRouterClaudeDefaultInterruptAndGoals(t *testing.T) {
	setupFakeAgents(t)
	cwd := t.TempDir()
	h := startRouterHarness(t, agentClaude)
	h.response(h.request("initialize", map[string]any{}))
	started := h.response(h.request("thread/start", map[string]any{"cwd": cwd}))
	result := started["result"].(map[string]any)
	thread := result["thread"].(map[string]any)["id"].(string)
	if result["model"] != claudeDefaultModel || len(thread) != 36 {
		t.Fatalf("claude thread: %v", result)
	}
	goal := h.response(h.request("thread/goal/get", map[string]any{"threadId": thread}))
	if goal["error"] != nil || goal["result"].(map[string]any)["goal"] != nil {
		t.Fatalf("a new Claude conversation has no goal: %v", goal)
	}
	h.request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "SLOW job"}}})
	h.waitFor(isMethod("item/agentMessage/delta"))
	h.response(h.request("turn/interrupt", map[string]any{"threadId": thread, "turnId": "x"}))
	done, _ := h.waitFor(isMethod("turn/completed"))
	if status := done["params"].(map[string]any)["turn"].(map[string]any)["status"]; status != "interrupted" {
		t.Fatalf("interrupt status %v", status)
	}
	h.response(h.request("thread/settings/update", map[string]any{"threadId": thread, "collaborationMode": map[string]any{"mode": "plan"}}))
	h.request("turn/start", map[string]any{"threadId": thread, "model": "claude/opus", "effort": "high", "input": []any{map[string]any{"type": "text", "text": "plan it"}}})
	_, seen := h.waitFor(isMethod("turn/completed"))
	if !strings.Contains(agentMessages(seen), "claude: plan it") {
		t.Fatalf("turn after restart: %v", seen)
	}
	args := readFakeLog(t, "claude-args")
	if len(args) != 2 || !strings.Contains(args[1], "--resume "+thread) || !strings.Contains(args[1], "--effort high") || !strings.Contains(args[1], "--model opus") || !strings.Contains(args[1], "--permission-mode plan") {
		t.Fatalf("effort change must restart on the same session: %q", args)
	}
	if controls := strings.Join(readFakeLog(t, "claude-control"), ","); !strings.Contains(controls, "interrupt") || !strings.Contains(controls, "set_permission_mode") {
		t.Fatalf("control requests: %s", controls)
	}
}

func TestTranscriptLabelsFollowTheActiveAgent(t *testing.T) {
	output := &chatTerminalOutput{}
	u := &chatUI{out: output}
	u.setEffectiveModel("gpt-x")
	output.WriteChatRole("assistant", "from codex\n")
	u.setEffectiveModel("claude/sonnet")
	output.WriteChatRole("assistant", "from claude\n")
	var labels []string
	for _, line := range output.transcriptLines(80) {
		if line.kind == "role-assistant" {
			labels = append(labels, line.text)
		}
	}
	if len(labels) != 2 || !strings.Contains(labels[0], "Codex") || !strings.Contains(labels[1], "Claude Code") {
		t.Fatalf("assistant labels %q", labels)
	}
	if got := u.effectiveModelLabel(); got != "Claude Sonnet" {
		t.Fatalf("model label %q", got)
	}
	if relativeToWorkspace("/w/s/a.go", "/w") != "s/a.go" || relativeToWorkspace("/x/a.go", "/w") != "/x/a.go" {
		t.Fatal("relative change paths")
	}
}

func TestAgentRouterClaudeGoals(t *testing.T) {
	setupFakeAgents(t)
	cwd := t.TempDir()
	h := startRouterHarness(t, agentClaude)
	h.response(h.request("initialize", map[string]any{}))
	started := h.response(h.request("thread/start", map[string]any{"cwd": cwd}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	set := h.response(h.request("thread/goal/set", map[string]any{"threadId": thread, "objective": "ship it", "status": "active"}))
	if set["result"].(map[string]any)["goal"].(map[string]any)["status"] != "active" {
		t.Fatalf("goal set: %v", set)
	}
	// The router keeps working toward the goal until Claude reports completion.
	complete, seen := h.waitFor(func(m map[string]any) bool {
		if m["method"] != "thread/goal/updated" {
			return false
		}
		return m["params"].(map[string]any)["goal"].(map[string]any)["status"] == "complete"
	})
	goal := complete["params"].(map[string]any)["goal"].(map[string]any)
	if goal["tokensUsed"].(float64) != 30 {
		t.Fatalf("goal token accounting: %v", goal)
	}
	turns, thinking := 0, false
	for _, m := range seen {
		if m["method"] == "turn/started" {
			turns++
		}
		if m["method"] == "jara/activity" && strings.Contains(m["params"].(map[string]any)["label"].(string), "~120 tokens") {
			thinking = true
		}
	}
	if turns != 2 || !thinking {
		t.Fatalf("expected two automatic turns with thinking progress, got %d turns, thinking %v", turns, thinking)
	}
	prompts := readFakeLog(t, "claude-prompt")
	if !strings.Contains(prompts[0], "[Active goal: ship it") {
		t.Fatalf("goal preamble missing: %q", prompts[0])
	}

	// A user message while a continuation runs waits for it instead of failing.
	h.response(h.request("thread/goal/set", map[string]any{"threadId": thread, "objective": "second goal", "status": "active"}))
	h.waitFor(isMethod("turn/started"))
	id := h.request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "user says hi"}}})
	reply := h.response(id)
	if reply["error"] != nil {
		t.Fatalf("queued user turn failed: %v", reply)
	}
	h.waitFor(func(m map[string]any) bool {
		return m["method"] == "item/agentMessage/delta" && strings.Contains(m["params"].(map[string]any)["delta"].(string), "user says hi")
	})

	// Switching to Codex carries the active goal.
	h.response(h.request("thread/goal/set", map[string]any{"threadId": thread, "status": "paused"}))
	h.request("turn/start", map[string]any{"threadId": thread, "model": "gpt-x", "input": []any{map[string]any{"type": "text", "text": "codex now"}}})
	h.waitFor(func(m map[string]any) bool {
		return m["method"] == "turn/completed" && m["params"].(map[string]any)["turn"].(map[string]any)["id"] == "ct"
	})
	if goals := readFakeLog(t, "codex-goal"); len(goals) != 1 || !strings.Contains(goals[0], "second goal|paused|marker=false") {
		t.Fatalf("goal carried to Codex: %q", goals)
	}
	if turns := readFakeLog(t, "codex-turn"); !strings.Contains(turns[len(turns)-1], "user says hi") {
		t.Fatalf("briefing must include the Claude part: %q", turns)
	}
}

func TestRoutedConversationsInBrowser(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	claudeThread := &routedThread{ID: "c-1", CWD: "/w", Segments: []threadSegment{{Agent: agentClaude, BackendID: "c-1", Started: true}}, Turns: []transcriptTurn{{User: "hello claude"}}}
	claudeThread.save()
	mixed := &routedThread{ID: "codex-a", CWD: "/w", Segments: []threadSegment{{Agent: agentCodex, BackendID: "codex-a"}, {Agent: agentClaude, BackendID: "c-2"}, {Agent: agentCodex, BackendID: "codex-b"}}}
	mixed.save()
	listed, err := routedConversations{}.List(context.Background())
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed %v %v", listed, err)
	}
	all := hideRoutedSegments(append(listed, Conversation{ID: "codex-a", Agent: "Codex"}, Conversation{ID: "codex-b", Agent: "Codex"}, Conversation{ID: "other", Agent: "Codex"}))
	var ids []string
	for _, c := range all {
		ids = append(ids, c.ID+"="+c.Agent)
	}
	if got := strings.Join(ids, ","); !strings.Contains(got, "c-1=Claude Code") || !strings.Contains(got, "codex-a=Codex (switched)") || !strings.Contains(got, "other=Codex") || strings.Contains(got, "codex-b") || strings.Count(got, "codex-a") != 1 {
		t.Fatalf("browser entries %s", got)
	}
	command, err := resumeConversationCommand("Claude Code", "c-1", "/w", []string{"--model", "x"})
	if err != nil || strings.Join(command, " ") != claudeBinary+" --resume c-1" {
		t.Fatalf("claude resume %q %v", command, err)
	}
	command, _ = resumeConversationCommand("Codex (switched)", "codex-a", "/w", nil)
	if strings.Join(command, " ") != codexBinary+" resume codex-b --include-non-interactive -C /w" {
		t.Fatalf("mixed resume %q", command)
	}
	if conversationRuntime("Codex (switched)", "codex-a") != agentCodex || conversationRuntime("Claude Code", "c-1") != agentClaude {
		t.Fatal("conversation runtime")
	}
}

func TestAgentRouterPassesClaudeArguments(t *testing.T) {
	setupFakeAgents(t)
	t.Setenv("JARA_CLAUDE_ARGS", `["--fallback-model","haiku"]`)
	binary, args := agentServerCommand("/usr/local/bin/codex", []string{"app-server"}, agentClaude, []string{"--add-dir", "/data"})
	if binary == "" || strings.Join(args, " ") != `_agent-server --default-agent claude --claude-args ["--add-dir","/data"] -- /usr/local/bin/codex app-server` {
		t.Fatalf("agent server command %q", args)
	}
	h := startRouterHarness(t, agentClaude, "--add-dir", "/data")
	h.response(h.request("initialize", map[string]any{}))
	started := h.response(h.request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	h.request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "hi"}}})
	h.waitFor(isMethod("turn/completed"))
	if got := readFakeLog(t, "claude-args"); len(got) != 1 || !strings.Contains(got[0], "--fallback-model haiku --add-dir /data") {
		t.Fatalf("defaults then per-invocation arguments expected: %q", got)
	}
}

func TestResumedHistoryKeepsPerTurnAgents(t *testing.T) {
	thread := &routedThread{ID: "x", Turns: []transcriptTurn{{User: "a", Agent: agentCodex, Reply: "from codex"}, {User: "b", Agent: agentClaude, Reply: "from claude"}}}
	raw, _ := json.Marshal(thread.historyTurns())
	var turns []chatHistoryTurn
	if err := json.Unmarshal(raw, &turns); err != nil {
		t.Fatal(err)
	}
	output := &chatTerminalOutput{}
	u := &chatUI{out: output, items: map[string]json.RawMessage{}}
	u.setEffectiveModel("claude/opus")
	u.renderChatHistory(turns)
	var labels []string
	for _, line := range output.transcriptLines(80) {
		if line.kind == "role-assistant" {
			labels = append(labels, line.text)
		}
	}
	if len(labels) != 2 || !strings.Contains(labels[0], "Codex") || !strings.Contains(labels[1], "Claude Code") {
		t.Fatalf("history labels %q", labels)
	}
}

func (h *routerHarness) raw(id string, method string, params any) map[string]any {
	h.send(map[string]any{"id": id, "method": method, "params": params})
	m, _ := h.waitFor(func(m map[string]any) bool { return m["id"] == id && m["method"] == nil })
	return m
}

func TestCrunchWorksWithClaudeConversations(t *testing.T) {
	setupFakeAgents(t)
	h := startRouterHarness(t, agentClaude)
	h.response(h.request("initialize", map[string]any{}))
	started := h.response(h.request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)

	// /goalcrunch sets the goal with a driver marker: the router must not
	// continue on its own, because crunch starts every turn itself.
	set := h.response(h.request("thread/goal/set", map[string]any{"threadId": thread, "objective": "LIMIT work", "status": "active", "jaraDriver": "crunch"}))
	if set["error"] != nil {
		t.Fatalf("goal set: %v", set)
	}
	turn := h.raw("jara-crunch-j1-1", "turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "Work toward the active goal: LIMIT work"}}})
	if turn["error"] != nil {
		t.Fatalf("crunch turn: %v", turn)
	}
	done, _ := h.waitFor(isMethod("turn/completed"))
	failure := done["params"].(map[string]any)["turn"].(map[string]any)["error"].(map[string]any)
	info, _ := json.Marshal(failure["codexErrorInfo"])
	if !chatCrunchIsUsageLimit(info, failure["message"].(string)) {
		t.Fatalf("crunch must recognize Claude's usage limit: %v", failure)
	}
	update, _ := h.waitFor(isMethod("thread/goal/updated"))
	if status := update["params"].(map[string]any)["goal"].(map[string]any)["status"]; status != "usageLimited" {
		t.Fatalf("goal status after the limit: %v", status)
	}

	limits := h.raw("jara-crunch-j1-2", "account/rateLimits/read", map[string]any{"threadId": thread})
	encoded, _ := json.Marshal(limits["result"])
	blocked, reset, err := chatCrunchRateLimitState(encoded, time.Now())
	if err != nil || !blocked || reset.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("Claude limits for crunch: blocked=%v reset=%v err=%v %s", blocked, reset, err, encoded)
	}

	// When Claude reports the window usable again, crunch is told to resume.
	h.response(h.request("thread/goal/set", map[string]any{"threadId": thread, "objective": "RESET check", "status": "active"}))
	h.waitFor(isMethod("account/rateLimits/updated"))
	h.waitFor(isMethod("turn/completed"))
	limits = h.raw("jara-crunch-j1-3", "account/rateLimits/read", map[string]any{"threadId": thread})
	encoded, _ = json.Marshal(limits["result"])
	if blocked, _, err = chatCrunchRateLimitState(encoded, time.Now()); err != nil || blocked {
		t.Fatalf("limits after reset: %v %v %s", blocked, err, encoded)
	}
	h.response(h.request("thread/goal/clear", map[string]any{"threadId": thread}))
	time.Sleep(2 * goalContinueDelay)

	// A successful crunch-driven goal turn is not followed by a router turn.
	h.response(h.request("thread/goal/set", map[string]any{"threadId": thread, "objective": "plain work", "status": "active", "jaraDriver": "crunch"}))
	before := len(readFakeLog(t, "claude-prompt"))
	h.raw("jara-crunch-j2-1", "turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "plain crunch turn"}}})
	h.waitFor(isMethod("turn/completed"))
	time.Sleep(3 * goalContinueDelay)
	if after := len(readFakeLog(t, "claude-prompt")); after != before+1 {
		t.Fatalf("crunch-driven goal must not auto-continue: %d prompts after, %d before", after, before)
	}

	// Codex conversations still get Codex's own limits, without Jara fields.
	h.raw("jara-crunch-j1-4", "account/rateLimits/read", map[string]any{"threadId": "codex-thread-x"})
	if got := readFakeLog(t, "codex-ratelimits"); len(got) != 1 || strings.Contains(got[0], "threadId") {
		t.Fatalf("codex rate limit request: %q", got)
	}
}
