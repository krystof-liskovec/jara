package jara

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentfake"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentrouter"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// These tests run Jara's interface code against the agent router with fake
// agents; the router's own tests live in internal/agentrouter.

// The test binary doubles as the fake agent CLIs; see internal/agentfake.
func TestMain(m *testing.M) {
	agentfake.ServeIfRequested()
	os.Exit(m.Run())
}

func startRouterHarness(t *testing.T, defaultAgent string, claudeArgs ...string) *agentfake.Harness {
	t.Helper()
	return agentfake.StartHarness(t, func(input io.Reader, output io.Writer) error {
		return agentrouter.Run(input, output, agentfake.CodexCommand(), agentrouter.Options{DefaultAgent: defaultAgent, ClaudeArgs: claudeArgs})
	})
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
}

func TestRoutedConversationsInBrowser(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	claudeThread := &agentrouter.Thread{ID: "c-1", CWD: "/w", Segments: []agentrouter.ThreadSegment{{Agent: protocol.AgentClaude, BackendID: "c-1", Started: true}}, Turns: []agentrouter.TranscriptTurn{{User: "hello claude"}}}
	claudeThread.Save()
	mixed := &agentrouter.Thread{ID: "codex-a", CWD: "/w", Segments: []agentrouter.ThreadSegment{{Agent: protocol.AgentCodex, BackendID: "codex-a"}, {Agent: protocol.AgentClaude, BackendID: "c-2"}, {Agent: protocol.AgentCodex, BackendID: "codex-b"}}}
	mixed.Save()
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
	if err != nil || strings.Join(command, " ") != protocol.ClaudeBinary+" --resume c-1" {
		t.Fatalf("claude resume %q %v", command, err)
	}
	command, _ = resumeConversationCommand("Codex (switched)", "codex-a", "/w", nil)
	if strings.Join(command, " ") != protocol.CodexBinary+" resume codex-b --include-non-interactive -C /w" {
		t.Fatalf("mixed resume %q", command)
	}
	if conversationRuntime("Codex (switched)", "codex-a") != protocol.AgentCodex || conversationRuntime("Claude Code", "c-1") != protocol.AgentClaude {
		t.Fatal("conversation runtime")
	}
}

func TestAgentRouterPassesClaudeArguments(t *testing.T) {
	agentfake.Setup(t)
	t.Setenv("JARA_CLAUDE_ARGS", `["--fallback-model","haiku"]`)
	binary, args := agentServerCommand("/usr/local/bin/codex", []string{"app-server"}, protocol.AgentClaude, false, []string{"--add-dir", "/data"})
	if binary == "" || strings.Join(args, " ") != `_agent-server --default-agent claude --claude-args ["--add-dir","/data"] -- /usr/local/bin/codex app-server` {
		t.Fatalf("agent server command %q", args)
	}
	_, options, err := agentServerOptions(args[1:]) // after the _agent-server subcommand
	if err != nil {
		t.Fatal(err)
	}
	h := agentfake.StartHarness(t, func(input io.Reader, output io.Writer) error {
		return agentrouter.Run(input, output, agentfake.CodexCommand(), options)
	})
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	h.Request("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "hi"}}})
	h.WaitFor(agentfake.IsMethod("turn/completed"))
	if got := agentfake.ReadLog(t, "claude-args"); len(got) != 1 || !strings.Contains(got[0], "--fallback-model haiku --add-dir /data") {
		t.Fatalf("defaults then per-invocation arguments expected: %q", got)
	}
}

func TestResumedHistoryKeepsPerTurnAgents(t *testing.T) {
	thread := &agentrouter.Thread{ID: "x", Turns: []agentrouter.TranscriptTurn{{User: "a", Agent: protocol.AgentCodex, Reply: "from codex"}, {User: "b", Agent: protocol.AgentClaude, Reply: "from claude"}}}
	raw, _ := json.Marshal(thread.HistoryTurns())
	var turns []protocol.HistoryTurn
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

func TestCrunchWorksWithClaudeConversations(t *testing.T) {
	agentfake.Setup(t)
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Response(h.Request("initialize", map[string]any{}))
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": t.TempDir()}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)

	// /goalcrunch sets the goal with a driver marker: the router must not
	// continue on its own, because crunch starts every turn itself.
	set := h.Response(h.Request("thread/goal/set", map[string]any{"threadId": thread, "objective": "LIMIT work", "status": "active", "jaraDriver": "crunch"}))
	if set["error"] != nil {
		t.Fatalf("goal set: %v", set)
	}
	turn := h.Raw("jara-crunch-j1-1", "turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "Work toward the active goal: LIMIT work"}}})
	if turn["error"] != nil {
		t.Fatalf("crunch turn: %v", turn)
	}
	done, _ := h.WaitFor(agentfake.IsMethod("turn/completed"))
	failure := done["params"].(map[string]any)["turn"].(map[string]any)["error"].(map[string]any)
	info, _ := json.Marshal(failure["codexErrorInfo"])
	if !chatCrunchIsUsageLimit(info, failure["message"].(string)) {
		t.Fatalf("crunch must recognize Claude's usage limit: %v", failure)
	}
	update, _ := h.WaitFor(agentfake.IsMethod("thread/goal/updated"))
	if status := update["params"].(map[string]any)["goal"].(map[string]any)["status"]; status != "usageLimited" {
		t.Fatalf("goal status after the limit: %v", status)
	}

	limits := h.Raw("jara-crunch-j1-2", "account/rateLimits/read", map[string]any{"threadId": thread})
	encoded, _ := json.Marshal(limits["result"])
	blocked, reset, err := chatCrunchRateLimitState(encoded, time.Now())
	if err != nil || !blocked || reset.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("Claude limits for crunch: blocked=%v reset=%v err=%v %s", blocked, reset, err, encoded)
	}

	// When Claude reports the window usable again, crunch is told to resume.
	h.Response(h.Request("thread/goal/set", map[string]any{"threadId": thread, "objective": "RESET check", "status": "active"}))
	h.WaitFor(agentfake.IsMethod("account/rateLimits/updated"))
	h.WaitFor(agentfake.IsMethod("turn/completed"))
	limits = h.Raw("jara-crunch-j1-3", "account/rateLimits/read", map[string]any{"threadId": thread})
	encoded, _ = json.Marshal(limits["result"])
	if blocked, _, err = chatCrunchRateLimitState(encoded, time.Now()); err != nil || blocked {
		t.Fatalf("limits after reset: %v %v %s", blocked, err, encoded)
	}
	h.Response(h.Request("thread/goal/clear", map[string]any{"threadId": thread}))
	time.Sleep(2 * agentrouter.GoalContinueDelay)

	// A successful crunch-driven goal turn is not followed by a router turn.
	h.Response(h.Request("thread/goal/set", map[string]any{"threadId": thread, "objective": "plain work", "status": "active", "jaraDriver": "crunch"}))
	before := len(agentfake.ReadLog(t, "claude-prompt"))
	h.Raw("jara-crunch-j2-1", "turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": "plain crunch turn"}}})
	h.WaitFor(agentfake.IsMethod("turn/completed"))
	time.Sleep(3 * agentrouter.GoalContinueDelay)
	if after := len(agentfake.ReadLog(t, "claude-prompt")); after != before+1 {
		t.Fatalf("crunch-driven goal must not auto-continue: %d prompts after, %d before", after, before)
	}

	// Codex conversations still get Codex's own limits, without Jara fields.
	h.Raw("jara-crunch-j1-4", "account/rateLimits/read", map[string]any{"threadId": "codex-thread-x"})
	if got := agentfake.ReadLog(t, "codex-ratelimits"); len(got) != 1 || strings.Contains(got[0], "threadId") {
		t.Fatalf("codex rate limit request: %q", got)
	}
}
