package agentrouter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func TestCodexTurnContinuesOnTheNextAccount(t *testing.T) {
	isolateAccounts(t)
	signInCodex(t, "a1", "a@example.com")
	RememberAccounts()
	signInCodex(t, "b2", "b@example.com")
	RememberAccounts()

	toUI, toCodex := &lockedBuffer{}, &bytes.Buffer{}
	r := &agentRouter{out: toUI, codexIn: json.NewEncoder(toCodex), codexCalls: map[string]codexCall{},
		threads: map[string]*Thread{}, codexToVirtual: map[string]string{}, codexChildren: map[string]bool{}}
	fromCodex := func(method, params string) {
		t.Helper()
		if err := r.fromCodex(rpcMessage{Method: method, Params: json.RawMessage(params)}); err != nil {
			t.Fatal(err)
		}
	}
	sentUI := func() string {
		toUI.mu.Lock()
		defer toUI.mu.Unlock()
		return toUI.b.String()
	}
	reset := time.Now().Add(2 * time.Hour).Unix()
	fromCodex("account/rateLimits/updated", `{"rateLimits":{"primary":{"usedPercent":100,"resetsAt":`+itoa(reset)+`}}}`)
	fromCodex("turn/started", `{"threadId":"th","turn":{"id":"T1","status":"inProgress"}}`)
	fromCodex("error", `{"threadId":"th","turnId":"T1","willRetry":false,"error":{"message":"You've hit your usage limit.","codexErrorInfo":"usageLimitExceeded"}}`)
	fromCodex("turn/completed", `{"threadId":"th","turn":{"id":"T1","status":"failed","error":{"message":"You've hit your usage limit.","codexErrorInfo":"usageLimitExceeded"}}}`)

	// The failure is held back, the account switched and a continuation sent.
	if ui := sentUI(); strings.Contains(ui, "usage limit") || !strings.Contains(ui, `"jara/account/switched"`) ||
		!strings.Contains(ui, `"from":"b@example.com (pro)"`) || !strings.Contains(ui, `"to":"a@example.com (pro)"`) {
		t.Fatalf("to the interface: %s", ui)
	}
	if activeCodex(t) != "a@example.com (pro)" {
		t.Fatalf("active %q", activeCodex(t))
	}
	var start struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
			Input    []struct{ Text string }
		} `json:"params"`
	}
	_ = json.Unmarshal(toCodex.Bytes(), &start)
	if start.Method != "turn/start" || start.Params.ThreadID != "th" || start.Params.Input[0].Text != AccountContinuation {
		t.Fatalf("continuation %s", toCodex)
	}
	// Codex runs it as T2; clients see T1 go on, without the hidden message.
	_ = r.fromCodex(rpcMessage{ID: start.ID, Result: json.RawMessage(`{"turn":{"id":"T2"}}`)})
	before := len(sentUI())
	fromCodex("turn/started", `{"threadId":"th","turn":{"id":"T2","status":"inProgress"}}`)
	fromCodex("item/started", `{"threadId":"th","turnId":"T2","item":{"type":"userMessage","content":[{"type":"text","text":"`+AccountContinuation+`"}]}}`)
	fromCodex("item/completed", `{"threadId":"th","turnId":"T2","item":{"type":"agentMessage","text":"Done."}}`)

	// An interrupt for T1 reaches the running T2.
	toCodex.Reset()
	if err := r.fromUI(rpcMessage{ID: json.RawMessage(`7`), Method: "turn/interrupt", Params: json.RawMessage(`{"threadId":"th","turnId":"T1"}`)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toCodex.String(), `"turnId":"T2"`) {
		t.Fatalf("interrupt went %s", toCodex)
	}
	fromCodex("turn/completed", `{"threadId":"th","turn":{"id":"T2","status":"completed"}}`)

	var shown []string
	scanner := bufio.NewScanner(strings.NewReader(sentUI()[before:]))
	for scanner.Scan() {
		shown = append(shown, scanner.Text())
	}
	if len(shown) != 2 || !strings.Contains(shown[0], `"turnId":"T1"`) || !strings.Contains(shown[0], "Done.") ||
		!strings.Contains(shown[1], `"turn":{"id":"T1","status":"completed"}`) || strings.Contains(sentUI(), "T2") {
		t.Fatalf("after the switch: %q", shown)
	}
	if len(r.switches.alias) != 0 || len(r.switches.latest) != 0 {
		t.Fatalf("left over %+v", r.switches)
	}
	for _, account := range Accounts() {
		if account.Label == "b@example.com (pro)" && account.ExhaustedUntil != reset {
			t.Fatalf("b out until %d, want %d", account.ExhaustedUntil, reset)
		}
	}

	// With no account left, the failure reaches the clients as before.
	fromCodex("turn/started", `{"threadId":"th","turn":{"id":"T3","status":"inProgress"}}`)
	fromCodex("error", `{"threadId":"th","turnId":"T3","willRetry":false,"error":{"message":"You've hit your usage limit.","codexErrorInfo":"usageLimitExceeded"}}`)
	fromCodex("turn/completed", `{"threadId":"th","turn":{"id":"T3","status":"failed","error":{"message":"You've hit your usage limit.","codexErrorInfo":"usageLimitExceeded"}}}`)
	if ui := sentUI(); !strings.Contains(ui, `"error","params"`) && !strings.Contains(ui, `"method":"error"`) || !strings.Contains(ui, `"id":"T3","status":"failed"`) {
		t.Fatalf("last failure: %s", ui)
	}
	_ = protocol.AgentCodex
}

func itoa(n int64) string {
	data, _ := json.Marshal(n)
	return string(data)
}

func TestClaudeTurnContinuesOnTheNextAccount(t *testing.T) {
	isolateAccounts(t)
	dir := t.TempDir()
	script := dir + "/claude"
	// Records each start's token and arguments, and what it is sent.
	if err := os.WriteFile(script, []byte(`#!/bin/sh
n=$(ls "`+dir+`" | grep -c '^run')
echo "$CLAUDE_CODE_OAUTH_TOKEN $*" > "`+dir+`/run$n"
cat > "`+dir+`/input$n"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JARA_CLAUDE_BIN", script)
	for _, token := range []string{"sk-ant-oat01-first", "sk-ant-oat01-second"} {
		if err := StoreClaudeToken(token); err != nil {
			t.Fatal(err)
		}
	}
	toUI := &lockedBuffer{}
	r := &agentRouter{out: toUI, events: make(chan routerEvent, 64), threads: map[string]*Thread{}}
	thread := &Thread{ID: "th", CWD: dir}
	s := newClaudeSession(r, thread, "backend-1", true)
	if err := s.startProcess(); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if data, err := os.ReadFile(dir + "/" + name); err == nil && len(data) > 0 {
				return string(data)
			}
		}
		return ""
	}
	if first := read("run0"); !strings.HasPrefix(first, "sk-ant-oat01-second ") {
		t.Fatalf("first start %q", first)
	}
	s.openTurn(nil)
	turn := s.turnID
	r.claudeLimits = &claudeRateLimits{Status: "rejected", ResetsAt: time.Now().Add(time.Hour).Unix()}

	if !s.continueOnNextAccount() || s.turnID != turn {
		t.Fatalf("continued %v, turn %q → %q", s.turnID == turn, turn, s.turnID)
	}
	if second := read("run1"); !strings.HasPrefix(second, "sk-ant-oat01-first ") || !strings.Contains(second, "--resume backend-1") {
		t.Fatalf("after the switch %q", second)
	}
	_ = s.stdin.Close() // the fake saves what it was sent once its input ends
	if input := read("input1"); !strings.Contains(input, AccountContinuation) {
		t.Fatalf("sent %q", input)
	}
	s.stop()
	if ui := toUI.b.String(); !strings.Contains(ui, `"jara/account/switched"`) || strings.Contains(ui, "turn/completed") || r.claudeLimits != nil {
		t.Fatalf("to the interface: %s", ui)
	}
}
