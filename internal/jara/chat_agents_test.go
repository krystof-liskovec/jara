package jara

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func newAgentTestUI(t *testing.T) (*chatUI, *bytes.Buffer) {
	t.Helper()
	var wire bytes.Buffer
	u := &chatUI{c: &appConnection{encoder: json.NewEncoder(&wire)}, out: &bytes.Buffer{}, thread: "main-thread", threadReady: true,
		pending: map[string]chatPending{}, pendingTurnModels: map[string]chatTurnRequest{}, items: map[string]json.RawMessage{}}
	return u, &wire
}

func agentEvent(t *testing.T, u *chatUI, method string, params any) {
	t.Helper()
	data, _ := json.Marshal(params)
	if err := u.event(appMessage{Method: method, Params: data}); err != nil {
		t.Fatal(err)
	}
}

func lastWireRequest(t *testing.T, wire *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(wire.String()), "\n")
	var request map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &request); err != nil {
		t.Fatalf("wire %q: %v", wire.String(), err)
	}
	return request
}

func spawnTestAgent(t *testing.T, u *chatUI, id, name string) *chatAgent {
	t.Helper()
	agentEvent(t, u, "item/completed", map[string]any{"threadId": u.thread, "item": map[string]any{
		"id": "a-" + id, "type": "subAgentActivity", "kind": "started", "agentThreadId": id, "agentPath": name}})
	agentEvent(t, u, "item/started", map[string]any{"threadId": u.thread, "item": map[string]any{
		"id": "c-" + id, "type": "collabAgentToolCall", "tool": "spawnAgent", "status": "inProgress", "receiverThreadIds": []string{id}}})
	agent := u.agentByID[id]
	if agent == nil {
		t.Fatalf("subagent %s was not registered", id)
	}
	return agent
}

func TestSubagentsRegisterFromParentItems(t *testing.T) {
	u, _ := newAgentTestUI(t)
	agent := spawnTestAgent(t, u, "child-1", "tests")
	if agent.name != "tests" || agent.status != "running" || len(u.agents) != 1 {
		t.Fatalf("agent = %+v", agent)
	}
	agentEvent(t, u, "item/completed", map[string]any{"threadId": u.thread, "item": map[string]any{
		"id": "w", "type": "collabAgentToolCall", "tool": "wait", "status": "completed", "receiverThreadIds": []string{"child-1"},
		"agentsStates": map[string]any{"child-1": map[string]any{"status": "completed", "message": "done"}}}})
	if agent.status != "completed" {
		t.Fatalf("status after wait = %q", agent.status)
	}
	// History replay registers the same agent once.
	u.renderChatHistory([]protocol.HistoryTurn{{ID: "t", Items: []protocol.HistoryItem{
		{ID: "a", Type: "subAgentActivity", Kind: "started", AgentThreadID: "child-1", AgentPath: "tests"},
		{ID: "b", Type: "subAgentActivity", Kind: "started", AgentThreadID: "child-2", AgentPath: "docs"}}}})
	if len(u.agents) != 2 || u.agents[1].name != "docs" {
		t.Fatalf("agents after history = %d", len(u.agents))
	}
}

func TestSubagentTurnEventsStayOutOfTheConversation(t *testing.T) {
	u, _ := newAgentTestUI(t)
	agent := spawnTestAgent(t, u, "child-1", "tests")
	agent.status = "completed"
	agentEvent(t, u, "turn/started", map[string]any{"threadId": "child-1", "turn": map[string]any{"id": "ct"}})
	if u.turn != "" || u.attachedTurnActive || agent.status != "running" {
		t.Fatalf("child turn leaked into the conversation: turn=%q attached=%v status=%q", u.turn, u.attachedTurnActive, agent.status)
	}
	agentEvent(t, u, "item/agentMessage/delta", map[string]any{"threadId": "child-1", "delta": "child text"})
	if strings.Contains(u.out.(*bytes.Buffer).String(), "child text") {
		t.Fatal("a subagent's reply appeared in the main transcript")
	}
	agentEvent(t, u, "turn/completed", map[string]any{"threadId": "child-1", "turn": map[string]any{"id": "ct", "status": "failed"}})
	if agent.status != "failed" || !agent.stale {
		t.Fatalf("status = %q stale = %v", agent.status, agent.stale)
	}
}

func TestViewedSubagentIsReadAndRendered(t *testing.T) {
	u, wire := newAgentTestUI(t)
	agent := spawnTestAgent(t, u, "child-1", "agent child-1")
	u.showChatAgent("child-1")
	if err := u.pollChatAgent(time.Now()); err != nil {
		t.Fatal(err)
	}
	request := lastWireRequest(t, wire)
	params, _ := request["params"].(map[string]any)
	if request["method"] != "thread/read" || params["threadId"] != "child-1" || params["includeTurns"] != true {
		t.Fatalf("read request = %v", request)
	}
	if err := u.pollChatAgent(time.Now()); err != nil || strings.Count(wire.String(), "thread/read") != 1 {
		t.Fatalf("a second read was sent while one is pending: %v", err)
	}
	reply, _ := json.Marshal(map[string]any{"thread": map[string]any{"agentNickname": "Darwin", "status": map[string]any{"type": "idle"},
		"turns": []any{map[string]any{"id": "t1", "items": []any{
			map[string]any{"id": "u", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "check the tests"}}},
			map[string]any{"id": "c", "type": "commandExecution", "command": "go test ./...", "aggregatedOutput": "ok\n"},
			map[string]any{"id": "m", "type": "agentMessage", "text": "All tests pass."}}}}}})
	if err := u.event(appMessage{ID: json.RawMessage(`1`), Result: reply}); err != nil {
		t.Fatal(err)
	}
	if agent.name != "Darwin" || agent.status != "completed" || agent.reading || len(agent.turns) != 1 {
		t.Fatalf("agent after read = %+v", agent)
	}
	out := &chatTerminalOutput{}
	renderChatAgentTranscript(out, agent)
	rendered := func() string {
		var text strings.Builder
		for _, line := range out.transcriptLines(80) {
			text.WriteString(line.text + "\n")
		}
		return text.String()
	}
	for _, want := range []string{"check the tests", "▸ $ go test ./... · 1 line", "All tests pass."} {
		if !strings.Contains(rendered(), want) {
			t.Fatalf("rendered transcript lacks %q:\n%s", want, rendered())
		}
	}
	// The command's output opens with a click and stays open when the
	// transcript is read again.
	if strings.Contains(rendered(), "    ok") || !out.toggleTool("c") {
		t.Fatalf("command output shows before it is opened:\n%s", rendered())
	}
	renderChatAgentTranscript(out, agent)
	if !strings.Contains(rendered(), "▾ $ go test ./...") || !strings.Contains(rendered(), "    ok") {
		t.Fatalf("opened command lacks its output:\n%s", rendered())
	}
}

func TestMessagingCodexSubagentUsesItsThread(t *testing.T) {
	u, wire := newAgentTestUI(t)
	agent := spawnTestAgent(t, u, "child-1", "tests")
	if err := u.messageChatAgent(agent, "also check docs"); err != nil {
		t.Fatal(err)
	}
	request := lastWireRequest(t, wire)
	params, _ := request["params"].(map[string]any)
	if request["method"] != "turn/start" || params["threadId"] != "child-1" || params["jaraSubagent"] != true {
		t.Fatalf("message request = %v", request)
	}
	if u.agentTurnActive() {
		t.Fatal("a message to a subagent counted as the conversation's turn")
	}
}

func TestMessagingClaudeSubagentRelaysThroughMainAgent(t *testing.T) {
	u, wire := newAgentTestUI(t)
	id := protocol.ClaudeSubagentThreadPrefix + "toolu_1"
	agent := spawnTestAgent(t, u, id, "Find tests")
	agent.agentID = "a1b2"
	if err := u.messageChatAgent(agent, "look in cmd too"); err != nil {
		t.Fatal(err)
	}
	request := lastWireRequest(t, wire)
	params, _ := request["params"].(map[string]any)
	text := jsonString(t, params["input"])
	if request["method"] != "turn/start" || params["threadId"] != "main-thread" ||
		!strings.Contains(text, "SendMessage") || !strings.Contains(text, "a1b2") || !strings.Contains(text, "look in cmd too") {
		t.Fatalf("relay request = %v", request)
	}

	// While the main agent works, the relay waits in its queue.
	u.turn = "busy"
	wire.Reset()
	if err := u.messageChatAgent(agent, "and scripts"); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || len(u.queuedPrompts) != 1 || !strings.Contains(u.queuedPrompts[0].text, "and scripts") {
		t.Fatalf("relay while busy: wire=%q queue=%v", wire.String(), u.queuedPrompts)
	}
}

func TestAgentStripAndSwitching(t *testing.T) {
	u, _ := newAgentTestUI(t)
	for _, name := range []string{"one", "two", "three", "four", "five", "six"} {
		spawnTestAgent(t, u, "child-"+name, name)
	}
	lines, targets := chatAgentStrip(u, time.Now())
	if len(lines) != 1+chatAgentStripRows+1 || targets[0] != "main" || !strings.Contains(lines[len(lines)-1].text, "2 more") {
		t.Fatalf("strip = %v targets = %v", lines, targets)
	}
	u.cycleChatAgentView(1)
	if u.viewing != "child-one" {
		t.Fatalf("Ctrl+Down showed %q", u.viewing)
	}
	lines, targets = chatAgentStrip(u, time.Now())
	if targets[1] != "child-one" || !strings.HasPrefix(lines[1].text, "› ") {
		t.Fatalf("the viewed agent is not shown or marked: %v", lines)
	}
	u.cycleChatAgentView(-1)
	u.cycleChatAgentView(-1)
	if u.viewing != "child-six" {
		t.Fatalf("Ctrl+Up from main showed %q", u.viewing)
	}
	u.runChatAgentsCommand("/agents main")
	if u.agentViewRequest != "main" {
		t.Fatalf("/agents main = %q", u.agentViewRequest)
	}
}

func TestCtrlArrowsSwitchAgents(t *testing.T) {
	parser := newChatTerminalKeyParser(1024)
	for sequence, want := range map[string]int{"\x1b[1;5B": 1, "\x1b[1;5A": -1, "\x1b[57353;5u": 1} {
		events := parser.feed([]byte(sequence))
		if len(events) != 1 || events[0].agentStep != want {
			t.Fatalf("%q = %+v, want step %d", sequence, events, want)
		}
	}
}
