package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func idleCompactMessage(t *testing.T, raw string) appMessage {
	t.Helper()
	var message appMessage
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func newTestIdleCompactor(now *time.Time, config chatIdleCompactConfig) *chatIdleCompactor {
	c := newChatIdleCompactor()
	c.now = func() time.Time { return *now }
	c.config = func() chatIdleCompactConfig { return config }
	return c
}

func TestIdleCompactorCompactsLargeIdleConversationOnce(t *testing.T) {
	now := time.Unix(1_000, 0)
	c := newTestIdleCompactor(&now, chatIdleCompactConfig{})
	var started []string
	c.begin = func(requestID string, notice []byte) { started = append(started, requestID+" "+string(notice)) }
	var input bytes.Buffer
	var inputMu sync.Mutex
	idle := func() bool { return false }

	c.observe(idleCompactMessage(t, `{"method":"thread/tokenUsage/updated","params":{"threadId":"t1","tokenUsage":{"last":{"totalTokens":150000},"modelContextWindow":256000}}}`), "t1")
	c.observe(idleCompactMessage(t, `{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"a","status":"completed"}}}`), "t1")
	now = now.Add(chatIdleCompactCodexAfter - time.Second)
	if err := c.tick(&input, &inputMu, "codex", idle); err != nil || input.Len() != 0 {
		t.Fatalf("compacted before the delay: %q %v", input.String(), err)
	}
	now = now.Add(time.Second)
	if err := c.tick(&input, &inputMu, "codex", idle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.String(), `"method":"thread/compact/start"`) || !strings.Contains(input.String(), `"threadId":"t1"`) {
		t.Fatalf("compaction request = %q", input.String())
	}
	if len(started) != 1 || !c.running() {
		t.Fatalf("started notifications %v running %v", started, c.running())
	}

	// The compaction turn runs and completes; the next idle period does not
	// compact again until a real turn reports a large context.
	c.observe(idleCompactMessage(t, `{"id":"jara-idle-compact-1","result":{}}`), "t1")
	c.observe(idleCompactMessage(t, `{"method":"turn/started","params":{"threadId":"t1","turn":{"id":"b"}}}`), "t1")
	c.observe(idleCompactMessage(t, `{"method":"thread/tokenUsage/updated","params":{"threadId":"t1","tokenUsage":{"last":{"totalTokens":150000}}}}`), "t1")
	c.observe(idleCompactMessage(t, `{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"b","status":"completed"}}}`), "t1")
	if c.running() {
		t.Fatal("compaction still running after its turn completed")
	}
	input.Reset()
	now = now.Add(time.Hour)
	if err := c.tick(&input, &inputMu, "codex", idle); err != nil || input.Len() != 0 {
		t.Fatalf("compacted twice: %q %v", input.String(), err)
	}
}

func TestIdleCompactorSkipsSmallBusyDisabledAndOtherThreads(t *testing.T) {
	now := time.Unix(1_000, 0)
	var inputMu sync.Mutex
	usage := func(thread string, tokens int) appMessage {
		return idleCompactMessage(t, fmt.Sprintf(`{"method":"thread/tokenUsage/updated","params":{"threadId":%q,"tokenUsage":{"last":{"totalTokens":%d},"modelContextWindow":256000}}}`, thread, tokens))
	}
	completed := idleCompactMessage(t, `{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"a","status":"completed"}}}`)
	cases := map[string]struct {
		config chatIdleCompactConfig
		tokens int
		thread string
		busy   bool
	}{
		"small":        {tokens: 100000, thread: "t1"},
		"disabled":     {config: chatIdleCompactConfig{Disabled: true}, tokens: 200000, thread: "t1"},
		"busy":         {tokens: 200000, thread: "t1", busy: true},
		"other thread": {tokens: 200000, thread: "sub"},
		"longer delay": {config: chatIdleCompactConfig{After: map[string]string{"codex": "55m"}}, tokens: 200000, thread: "t1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			start := now
			c := newTestIdleCompactor(&start, tc.config)
			c.observe(usage(tc.thread, tc.tokens), "t1")
			c.observe(completed, "t1")
			start = start.Add(10 * time.Minute)
			var input bytes.Buffer
			if err := c.tick(&input, &inputMu, "codex", func() bool { return tc.busy }); err != nil || input.Len() != 0 {
				t.Fatalf("unexpected compaction %q %v", input.String(), err)
			}
		})
	}
}

func TestIdleCompactorRetriesAfterFailedStart(t *testing.T) {
	now := time.Unix(1_000, 0)
	c := newTestIdleCompactor(&now, chatIdleCompactConfig{})
	var input bytes.Buffer
	var inputMu sync.Mutex
	c.observe(idleCompactMessage(t, `{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"last":{"totalTokens":120000}}}}`), "t1")
	c.observe(idleCompactMessage(t, `{"method":"turn/completed","params":{"turn":{"id":"a","status":"completed"}}}`), "t1")
	now = now.Add(chatIdleCompactCodexAfter)
	if err := c.tick(&input, &inputMu, "codex", func() bool { return false }); err != nil || input.Len() == 0 {
		t.Fatalf("no compaction started: %v", err)
	}
	c.observe(idleCompactMessage(t, `{"id":"jara-idle-compact-1","error":{"code":1,"message":"nothing to compact"}}`), "t1")
	if c.running() {
		t.Fatal("a refused compaction still counts as running")
	}
	input.Reset()
	now = now.Add(time.Hour)
	if err := c.tick(&input, &inputMu, "codex", func() bool { return false }); err != nil || input.Len() != 0 {
		t.Fatal("a refused compaction is retried in the same idle period")
	}
}

func TestIdleCompactConfigRequests(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	for agent, want := range map[string]string{"": "25m0s", "codex": "25m0s", "claude": "55m0s"} {
		result, err := handleChatIdleCompactRequest("jara/idleCompaction/get", nil, agent)
		if err != nil || result.(map[string]any)["enabled"] != true || result.(map[string]any)["after"] != want {
			t.Fatalf("default config for %q = %v %v", agent, result, err)
		}
	}
	if _, err := handleChatIdleCompactRequest("jara/idleCompaction/set", json.RawMessage(`{"after":"10s"}`), "codex"); err == nil {
		t.Fatal("accepted a delay below the minimum")
	}
	result, err := handleChatIdleCompactRequest("jara/idleCompaction/set", json.RawMessage(`{"enabled":false,"after":"20m"}`), "codex")
	if err != nil || result.(map[string]any)["enabled"] != false || result.(map[string]any)["after"] != "20m0s" {
		t.Fatalf("set config = %v %v", result, err)
	}
	config := loadChatIdleCompactConfig()
	if !config.Disabled || config.after("codex") != 20*time.Minute || config.after("claude") != chatIdleCompactClaudeAfter {
		t.Fatalf("saved config = %+v", config)
	}
}

func TestIdleCompactorWaitsLongerForClaude(t *testing.T) {
	now := time.Unix(1_000, 0)
	c := newTestIdleCompactor(&now, chatIdleCompactConfig{})
	var input bytes.Buffer
	var inputMu sync.Mutex
	idle := func() bool { return false }
	c.observe(idleCompactMessage(t, `{"method":"thread/tokenUsage/updated","params":{"threadId":"t1","tokenUsage":{"last":{"totalTokens":150000},"modelContextWindow":200000}}}`), "t1")
	c.observe(idleCompactMessage(t, `{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"a","status":"completed"}}}`), "t1")
	now = now.Add(10 * time.Minute)
	if err := c.tick(&input, &inputMu, "claude", idle); err != nil || input.Len() != 0 {
		t.Fatalf("Claude conversation compacted inside its one-hour cache: %q %v", input.String(), err)
	}
	now = now.Add(chatIdleCompactClaudeAfter)
	if err := c.tick(&input, &inputMu, "claude", idle); err != nil || !strings.Contains(input.String(), "thread/compact/start") {
		t.Fatalf("Claude conversation not compacted: %q %v", input.String(), err)
	}
}

func TestChatDaemonTracksConversationAgent(t *testing.T) {
	var stateMu, pendingMu sync.Mutex
	state := chatDaemonState{}
	pending := map[string]string{`"r1"`: "thread/resume", `"c1"`: "thread/compact/start"}
	applyChatDaemonEvent([]byte(`{"id":"r1","result":{"thread":{"id":"t1"},"model":"claude/opus"}}`), &state, &stateMu, &pending, &pendingMu)
	if state.Agent != "claude" || state.Conversation != "t1" {
		t.Fatalf("state after resume = %+v", state)
	}
	state.ActiveTurn = true
	applyChatDaemonEvent([]byte(`{"id":"c1","error":{"code":1,"message":"nothing to compact"}}`), &state, &stateMu, &pending, &pendingMu)
	if state.ActiveTurn {
		t.Fatal("a refused compaction left the turn active")
	}
	if model := chatDaemonRequestModel(json.RawMessage(`{"threadId":"t1","model":"gpt-5.5"}`)); protocol.AgentForModel(model) != "codex" {
		t.Fatalf("turn model %q", model)
	}
}
