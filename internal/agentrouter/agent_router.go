// Package agentrouter is the process between the Jara interface and the
// agents (`jara _agent-server`). It speaks the Codex app-server protocol to the
// interface, proxies Codex, runs Claude Code in stream-json mode behind the
// same protocol, and keeps conversations that switch between them.
package agentrouter

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"krystof.liskovec/jara/internal/protocol"
)

// The agent router is the app-server behind Jara's conversation UI. It speaks
// the Codex app-server protocol to the UI, proxies Codex conversations to the
// real `codex app-server`, and serves Claude Code conversations by driving
// `claude` in stream-json mode. A conversation can move between agents when
// the user picks another agent's model; the router keeps the UI's thread ID
// stable and briefs the new agent with the conversation so far.
type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type routerEvent struct {
	source    string // ui, codex, claude
	message   rpcMessage
	session   *claudeSession
	process   *exec.Cmd // Claude process that produced the event
	thread    string    // goal continuation target
	btwThread string    // side-question fork that timed out
	line      []byte
	exited    error
	closed    bool
}

type codexCall struct {
	uiID     json.RawMessage // nil for router-internal requests
	method   string
	callback func(result json.RawMessage, failure json.RawMessage)
}

type agentRouter struct {
	writeMu        sync.Mutex
	out            io.Writer
	events         chan routerEvent
	codexIn        *json.Encoder
	codexCmd       *exec.Cmd
	codexCalls     map[string]codexCall
	sequence       int
	defaultAgent   string
	agentExplicit  bool     // defaultAgent was chosen explicitly, not configured
	claudeArgs     []string // per-invocation Claude Code arguments
	claudeLimits   *claudeRateLimits
	threads        map[string]*Thread
	codexToVirtual map[string]string
	approvals      map[string]*claudeSession // UI request ID -> session awaiting a decision
	btw            map[string]*btwFork       // Codex fork thread -> pending side question
	btwForking     int                       // side-question forks awaiting their thread ID
}

func runAgentRouter(input io.Reader, output io.Writer, codexCommand []string, defaultAgent string, claudeArgs ...string) error {
	return runRouter(input, output, codexCommand, defaultAgent, false, claudeArgs...)
}

// Options configure the router for this host.
type Options struct {
	DefaultAgent  string   // agent new conversations start with
	AgentExplicit bool     // the agent was chosen explicitly, overriding the default model
	ClaudeArgs    []string // arguments for every Claude Code process, host defaults included
}

// Run serves one Jara interface on input and output until it disconnects,
// with codexCommand starting the Codex app-server.
func Run(input io.Reader, output io.Writer, codexCommand []string, options Options) error {
	return runRouter(input, output, codexCommand, options.DefaultAgent, options.AgentExplicit, options.ClaudeArgs...)
}

func runRouter(input io.Reader, output io.Writer, codexCommand []string, defaultAgent string, explicit bool, claudeArgs ...string) error {
	r := &agentRouter{claudeArgs: claudeArgs, out: output, events: make(chan routerEvent, 256), codexCalls: map[string]codexCall{},
		defaultAgent: defaultAgent, agentExplicit: explicit, threads: map[string]*Thread{}, codexToVirtual: map[string]string{},
		approvals: map[string]*claudeSession{}}
	if err := r.startCodex(codexCommand); err != nil {
		return err
	}
	defer r.shutdown()
	go readRPC(input, "ui", r.events)
	for event := range r.events {
		if event.closed && event.source == "ui" {
			return nil
		}
		if err := r.handle(event); err != nil {
			return err
		}
	}
	return nil
}

func readRPC(input io.Reader, source string, events chan<- routerEvent) {
	scanner := protocol.NewLineScanner(input)
	for scanner.Scan() {
		var message rpcMessage
		if json.Unmarshal(scanner.Bytes(), &message) == nil {
			events <- routerEvent{source: source, message: message}
		}
	}
	events <- routerEvent{source: source, closed: true}
}

func (r *agentRouter) startCodex(command []string) error {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}
	r.codexCmd, r.codexIn = cmd, json.NewEncoder(in)
	go readRPC(out, "codex", r.events)
	return nil
}

func (r *agentRouter) shutdown() {
	for _, thread := range r.threads {
		if thread.claude != nil {
			thread.claude.stop()
		}
	}
	if r.codexCmd != nil && r.codexCmd.Process != nil {
		_ = r.codexCmd.Process.Kill()
		_ = r.codexCmd.Wait()
	}
}

func (r *agentRouter) send(message any) {
	data, err := json.Marshal(message)
	if err != nil {
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_, _ = r.out.Write(append(data, '\n'))
}

func (r *agentRouter) reply(id json.RawMessage, result any) {
	r.send(map[string]any{"id": id, "result": result})
}

func (r *agentRouter) fail(id json.RawMessage, message string) {
	r.send(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": message}})
}

func (r *agentRouter) notify(method string, params any) {
	r.send(map[string]any{"method": method, "params": params})
}

// callCodex sends a request to Codex. UI requests keep their ID so replies
// can be forwarded; router requests use string IDs that cannot collide.
func (r *agentRouter) callCodex(call codexCall, params json.RawMessage) error {
	id := call.uiID
	if id == nil {
		r.sequence++
		id = json.RawMessage(strconv.Quote("jara-router-" + strconv.Itoa(r.sequence)))
	}
	r.codexCalls[string(id)] = call
	return r.codexIn.Encode(rpcMessage{ID: id, Method: call.method, Params: params})
}

func (r *agentRouter) handle(event routerEvent) error {
	switch event.source {
	case "codex":
		if event.closed {
			return fmt.Errorf("Codex app-server exited")
		}
		return r.fromCodex(event.message)
	case "goal":
		r.continueGoal(event.thread)
		return nil
	case "btw":
		r.finishBtw(event.btwThread, "", "the side question timed out")
		return nil
	case "claude":
		if event.process != event.session.cmd {
			return nil // output from a process that was replaced
		}
		if event.exited != nil || event.closed {
			event.session.processExited(event.exited)
			return nil
		}
		event.session.handleLine(event.line)
		return nil
	}
	return r.fromUI(event.message)
}

func (r *agentRouter) fromCodex(m rpcMessage) error {
	if m.Method == "" {
		call, ok := r.codexCalls[string(m.ID)]
		if !ok {
			return nil
		}
		delete(r.codexCalls, string(m.ID))
		if call.callback != nil {
			call.callback(m.Result, m.Error)
			return nil
		}
		m.Result = r.postProcessCodexResult(call, m.Result, m.Error)
		r.send(m)
		return nil
	}
	if r.captureBtw(m) {
		return nil
	}
	m.Params = r.rewriteCodexThread(m.Params)
	r.observeCodexNotification(m)
	r.send(m)
	return nil
}

// rewriteCodexThread maps a Codex thread ID back to the UI's thread ID after
// a conversation moved to Codex from another agent.
func (r *agentRouter) rewriteCodexThread(params json.RawMessage) json.RawMessage {
	if len(r.codexToVirtual) == 0 || len(params) == 0 {
		return params
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil {
		return params
	}
	var thread string
	if json.Unmarshal(fields["threadId"], &thread) != nil {
		return params
	}
	virtual, ok := r.codexToVirtual[thread]
	if !ok {
		return params
	}
	fields["threadId"], _ = json.Marshal(virtual)
	rewritten, err := json.Marshal(fields)
	if err != nil {
		return params
	}
	return rewritten
}

func (r *agentRouter) fromUI(m rpcMessage) error {
	if m.Method == "" {
		if session, ok := r.approvals[string(m.ID)]; ok {
			delete(r.approvals, string(m.ID))
			session.answerApproval(string(m.ID), m.Result)
			return nil
		}
		return r.codexIn.Encode(m) // reply to a Codex server request
	}
	if len(m.ID) == 0 {
		if m.Method == "initialized" {
			return r.codexIn.Encode(m)
		}
		return nil
	}
	params := m.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	switch m.Method {
	case "initialize":
		return r.callCodex(codexCall{uiID: m.ID, method: m.Method}, params)
	case "model/list":
		return r.callCodex(codexCall{uiID: m.ID, method: m.Method}, params)
	case "thread/list":
		return r.callCodex(codexCall{uiID: m.ID, method: m.Method}, params)
	case "thread/start":
		return r.startThread(m.ID, params)
	case "thread/resume":
		return r.resumeThread(m.ID, params)
	case "turn/start":
		return r.startTurn(m.ID, params)
	case "jara/btw":
		return r.askBtw(m.ID, params)
	case "jara/rewind":
		return r.rewindThread(m.ID, params)
	case "jara/commands/list":
		r.reply(m.ID, map[string]any{"commands": LoadCustomCommands(params)})
		return nil
	case "jara/defaultModel/get":
		return r.getDefaultModels(m.ID, params)
	case "jara/defaultModel/set":
		return r.setDefaultModel(m.ID, params)
	case "jara/fallbackModel/set":
		return r.setFallbackModel(m.ID, params)
	case "account/rateLimits/read":
		return r.readRateLimits(m.ID, params)
	case "thread/goal/set":
		params = r.noteGoalDriver(params)
	}
	thread := r.claudeThreadFor(params)
	if thread == nil {
		return r.callCodex(codexCall{uiID: m.ID, method: m.Method}, r.codexParams(params))
	}
	if thread.claude == nil { // released by /rewind
		thread.claude = newClaudeSession(r, thread, thread.Current().BackendID, thread.Current().Started)
	}
	switch m.Method {
	case "thread/compact/start":
		r.compactClaude(m.ID, thread)
	case "turn/interrupt":
		thread.claude.interrupt()
		r.reply(m.ID, map[string]any{})
	case "thread/settings/update":
		var request struct {
			CollaborationMode struct {
				Mode string `json:"mode"`
			} `json:"collaborationMode"`
		}
		_ = json.Unmarshal(params, &request)
		thread.claude.setPlanMode(request.CollaborationMode.Mode == "plan")
		r.reply(m.ID, map[string]any{})
	default:
		if strings.HasPrefix(m.Method, "thread/goal/") {
			r.handleClaudeGoal(m.ID, m.Method, params, thread)
			return nil
		}
		return r.callCodex(codexCall{uiID: m.ID, method: m.Method}, params)
	}
	return nil
}

// codexParams replaces the UI's thread ID with the Codex thread ID for
// conversations that moved to Codex.
func (r *agentRouter) codexParams(params json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil {
		return params
	}
	var id string
	if json.Unmarshal(fields["threadId"], &id) != nil {
		return params
	}
	thread := r.threads[id]
	if thread == nil || thread.Current().Agent != protocol.AgentCodex || thread.Current().BackendID == id {
		return params
	}
	fields["threadId"], _ = json.Marshal(thread.Current().BackendID)
	out, _ := json.Marshal(fields)
	return out
}

func (r *agentRouter) claudeThreadFor(params json.RawMessage) *Thread {
	var request struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &request)
	thread := r.threads[request.ThreadID]
	if thread == nil || thread.Current().Agent != protocol.AgentClaude {
		return nil
	}
	return thread
}
