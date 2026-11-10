package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// claudeSession runs one Claude Code conversation in stream-json mode:
// prompts and control requests go to stdin, events come from stdout, and
// permission checks arrive as can_use_tool control requests.
type claudeSession struct {
	r         *agentRouter
	thread    *routedThread
	backendID string
	resume    bool
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stderr    *tailBuffer
	model     string
	effort    string
	plan      bool

	turnID       string
	turnCount    int
	turnStarted  time.Time
	tokens       int64
	goalTurn     bool
	crunchTurn   bool // started by crunch, which drives the next turn itself
	usageLimited bool
	queued       []queuedClaudeTurn
	interrupted  bool
	reply        strings.Builder
	textItem     string
	blockText    strings.Builder
	itemCount    int
	tools        map[string]*claudeTool
	approvals    map[string]claudeApproval
	controlSeq   int
}

type claudeTool struct {
	id, name string
	input    map[string]any
	item     map[string]any
}

type queuedClaudeTurn struct {
	id      json.RawMessage
	message string
}

type claudeApproval struct {
	requestID string
	input     map[string]any
}

// claudeExecutable allows tests to substitute a fake CLI.
func claudeExecutable() string {
	if path := os.Getenv("JARA_CLAUDE_BIN"); path != "" {
		return path
	}
	return claudeBinary
}

func claudeAvailable() bool {
	_, err := os.Stat(claudeExecutable())
	return err == nil
}

func newClaudeSession(r *agentRouter, thread *routedThread, backendID string, resume bool) *claudeSession {
	return &claudeSession{r: r, thread: thread, backendID: backendID, resume: resume, tools: map[string]*claudeTool{}, approvals: map[string]claudeApproval{}}
}

type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 4096 {
		b.data = b.data[len(b.data)-4096:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

func (s *claudeSession) startProcess() error {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--permission-prompt-tool", "stdio"}
	if defaults, err := configuredAgentArgs(agentClaude, s.r.claudeArgs); err == nil {
		args = append(args, defaults...)
	}
	if s.resume {
		args = append(args, "--resume", s.backendID)
	} else {
		args = append(args, "--session-id", s.backendID)
	}
	s.model, s.effort = claudeCLIModel(s.thread.Model), s.thread.Effort
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.effort != "" {
		args = append(args, "--effort", s.effort)
	}
	if s.plan {
		args = append(args, "--permission-mode", "plan")
	}
	cmd := exec.Command(claudeExecutable(), args...)
	cmd.Dir = s.thread.CWD
	s.stderr = &tailBuffer{}
	cmd.Stderr = s.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start Claude Code: %w", err)
	}
	s.cmd, s.stdin, s.resume = cmd, stdin, true
	events := s.r.events
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 32<<20)
		for scanner.Scan() {
			events <- routerEvent{source: "claude", session: s, process: cmd, line: append([]byte(nil), scanner.Bytes()...)}
		}
		events <- routerEvent{source: "claude", session: s, process: cmd, exited: fmt.Errorf("exit: %v", cmd.Wait())}
	}()
	return nil
}

func (s *claudeSession) write(message any) error {
	if s.stdin == nil {
		return fmt.Errorf("Claude Code is not running")
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

func (s *claudeSession) control(subtype string, fields map[string]any) {
	s.controlSeq++
	request := map[string]any{"subtype": subtype}
	for key, value := range fields {
		request[key] = value
	}
	_ = s.write(map[string]any{"type": "control_request", "request_id": "jara-" + strconv.Itoa(s.controlSeq), "request": request})
}

// ensureProcess starts Claude Code or adapts the running process to the
// thread's model and effort. Effort has no runtime control, so changing it
// restarts the process on the same session.
func (s *claudeSession) ensureProcess() error {
	model, effort := claudeCLIModel(s.thread.Model), s.thread.Effort
	if s.cmd != nil && (effort != s.effort || model == "" && s.model != "") {
		s.stop()
	}
	if s.cmd == nil {
		return s.startProcess()
	}
	if model != s.model {
		s.control("set_model", map[string]any{"model": model})
		s.model = model
	}
	return nil
}

func (s *claudeSession) startTurn(id json.RawMessage, message string) error {
	if s.turnID != "" {
		if s.goalTurn && id != nil {
			// The user's message runs right after the goal continuation.
			s.queued = append(s.queued, queuedClaudeTurn{id: id, message: message})
			return nil
		}
		if id != nil {
			s.r.fail(id, "a Claude Code turn is already running")
		}
		return nil
	}
	if id != nil {
		s.goalTurn = false
		s.crunchTurn = strings.HasPrefix(string(id), `"jara-crunch-`)
	}
	if err := s.ensureProcess(); err != nil {
		if id != nil {
			s.r.fail(id, err.Error())
		}
		return nil
	}
	s.turnCount++
	s.turnID = s.backendID + "-turn-" + strconv.Itoa(s.turnCount)
	s.interrupted = false
	s.reply.Reset()
	s.turnStarted, s.tokens = time.Now(), 0
	turn := map[string]any{"id": s.turnID, "status": "inProgress"}
	if id != nil {
		s.r.reply(id, map[string]any{"turn": turn})
	}
	s.r.notify("turn/started", map[string]any{"threadId": s.thread.ID, "turn": turn})
	for i := range s.thread.Segments {
		if s.thread.Segments[i].BackendID == s.backendID {
			s.thread.Segments[i].Started = true
		}
	}
	s.thread.save()
	if err := s.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": message}}}}); err != nil {
		s.finishTurn("failed", "send prompt to Claude Code: "+err.Error())
	}
	return nil
}

func (s *claudeSession) interrupt() {
	if s.turnID == "" || s.cmd == nil {
		return
	}
	s.interrupted = true
	s.control("interrupt", nil)
}

func (s *claudeSession) setPlanMode(plan bool) {
	s.plan = plan
	if s.cmd != nil {
		mode := "default"
		if plan {
			mode = "plan"
		}
		s.control("set_permission_mode", map[string]any{"mode": mode})
	}
	s.r.notify("thread/settings/updated", map[string]any{"threadId": s.thread.ID, "threadSettings": map[string]any{"collaborationMode": map[string]any{"mode": map[bool]string{true: "plan", false: "default"}[plan]}}})
}

func (s *claudeSession) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.stdin.Close()
		_ = s.cmd.Process.Kill()
	}
	s.cmd, s.stdin = nil, nil
}

func (s *claudeSession) processExited(err error) {
	if s.cmd == nil {
		return // stopped deliberately
	}
	s.cmd, s.stdin = nil, nil
	if s.turnID != "" {
		detail := s.stderr.String()
		if detail == "" && err != nil {
			detail = err.Error()
		}
		s.finishTurn("failed", "Claude Code exited unexpectedly: "+detail)
	}
}

func (s *claudeSession) finishTurn(status, message string) {
	if s.turnID == "" {
		return
	}
	s.closeTextItem()
	if turn := s.thread.lastTurn(); turn != nil && s.reply.Len() > 0 {
		turn.Reply = s.reply.String()
	}
	s.thread.save()
	turn := map[string]any{"id": s.turnID, "status": status}
	if message != "" {
		failure := map[string]any{"message": message}
		if s.usageLimited {
			failure["codexErrorInfo"] = "usageLimitExceeded" // lets crunch wait for the reset
		}
		turn["error"] = failure
	}
	for id := range s.approvals {
		delete(s.r.approvals, id)
	}
	s.approvals = map[string]claudeApproval{}
	s.tools = map[string]*claudeTool{}
	s.turnID = ""
	s.r.notify("turn/completed", map[string]any{"threadId": s.thread.ID, "turn": turn})
	s.r.afterClaudeTurn(s, status, s.reply.String(), s.tokens, time.Since(s.turnStarted))
	s.goalTurn, s.usageLimited = false, false
}
