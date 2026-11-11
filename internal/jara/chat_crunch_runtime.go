package jara

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentrouter"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

type chatCrunchJob struct {
	ID         string    `json:"id"`
	ThreadID   string    `json:"threadId"`
	Mode       string    `json:"mode"`
	Prompt     string    `json:"prompt"`
	Status     string    `json:"status"`
	TurnID     string    `json:"turnId,omitempty"`
	GoalStatus string    `json:"goalStatus,omitempty"`
	Error      string    `json:"error,omitempty"`
	ResetAt    time.Time `json:"resetAt,omitempty"`
	Started    bool      `json:"started,omitempty"`
	Fallback   string    `json:"fallback,omitempty"` // model to continue on at a usage limit
	FellBack   bool      `json:"fellBack,omitempty"` // turns now use the fallback model
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type chatCrunchManager struct {
	mu          sync.Mutex
	path        string
	job         *chatCrunchJob
	requestID   string
	requestKind string
	sequence    uint64
}

func newChatCrunchManager(path string) (*chatCrunchManager, error) {
	m := &chatCrunchManager{path: path}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("crunch store is not a regular file")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		var saved chatCrunchJob
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, fmt.Errorf("read crunch task: %w", err)
		}
		m.job = &saved
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if m.job != nil {
		switch m.job.Status {
		case "running":
			m.job.Status = "pending"
			m.job.Started = true
			m.job.TurnID = ""
			m.job.Error = "Jara restarted during this task; continuing from the saved conversation."
			m.touchLocked()
			if err := m.saveLocked(); err != nil {
				return nil, err
			}
		case "stopping":
			m.job.Status = "stopped"
			m.job.TurnID = ""
			m.touchLocked()
			if err := m.saveLocked(); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

func (m *chatCrunchManager) handleClientRequest(method string, params json.RawMessage, currentThread string, input io.Writer, inputMu *sync.Mutex) (any, error) {
	switch method {
	case "jara/crunch/create":
		var request struct {
			ThreadID string `json:"threadId"`
			Mode     string `json:"mode"`
			Prompt   string `json:"prompt"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, fmt.Errorf("invalid crunch request: %w", err)
		}
		if request.ThreadID == "" || request.ThreadID != currentThread {
			return nil, fmt.Errorf("the conversation changed; retry the crunch command in this conversation")
		}
		job, err := m.create(request.ThreadID, request.Mode, request.Prompt)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": job}, nil
	case "jara/crunch/status":
		return map[string]any{"job": m.current()}, nil
	case "jara/crunch/stop":
		if err := m.stop(input, inputMu, currentThread); err != nil {
			return nil, err
		}
		return map[string]any{"job": m.current()}, nil
	default:
		return nil, fmt.Errorf("unsupported crunch operation")
	}
}

func (m *chatCrunchManager) create(threadID, mode, prompt string) (chatCrunchJob, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return chatCrunchJob{}, fmt.Errorf("crunch prompt cannot be empty")
	}
	if mode != "prompt" && mode != "goal" {
		return chatCrunchJob{}, fmt.Errorf("unsupported crunch mode")
	}
	if mode == "goal" && utf8.RuneCountInString(prompt) > 4000 {
		return chatCrunchJob{}, fmt.Errorf("goal objectives must be 4,000 characters or fewer")
	}
	if len(prompt) > maxBackgroundPrompt {
		return chatCrunchJob{}, fmt.Errorf("crunch prompt exceeds 64 KiB")
	}
	var idBytes [5]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return chatCrunchJob{}, err
	}
	now := time.Now()
	job := &chatCrunchJob{
		ID: "cr-" + hex.EncodeToString(idBytes[:]), ThreadID: threadID,
		Mode: mode, Prompt: prompt, Status: "pending", CreatedAt: now, UpdatedAt: now,
		Fallback: agentrouter.LoadModelPrefs().Fallback,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && crunchJobActive(m.job.Status) {
		return chatCrunchJob{}, fmt.Errorf("crunch task %s is already %s", m.job.ID, m.job.Status)
	}
	previous := m.job
	m.job = job
	if err := m.saveLocked(); err != nil {
		m.job = previous
		return chatCrunchJob{}, err
	}
	return *job, nil
}

func (m *chatCrunchManager) current() *chatCrunchJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	copy := *m.job
	return &copy
}

func (m *chatCrunchManager) tick(input io.Writer, inputMu *sync.Mutex, busy func() bool) error {
	m.mu.Lock()
	if m.job == nil || m.requestID != "" {
		m.mu.Unlock()
		return nil
	}
	kind, method := "", ""
	var params map[string]any
	switch m.job.Status {
	case "pending":
		kind, method = "turnStart", "turn/start"
		text := crunchInitialPrompt(*m.job)
		if m.job.Started {
			text = crunchContinuationPrompt(*m.job)
		}
		params = map[string]any{
			"threadId": m.job.ThreadID,
			"input":    []any{map[string]any{"type": "text", "text": text}},
			"summary":  "concise",
		}
		if m.job.FellBack {
			params["model"] = m.job.Fallback // the router switches providers
		}
		m.job.Status = "running"
	case "waitingUsage":
		if !m.job.ResetAt.IsZero() && time.Now().Before(m.job.ResetAt) {
			m.mu.Unlock()
			return nil
		}
		kind, method = "rateLimits", "account/rateLimits/read"
		params = map[string]any{"threadId": m.job.ThreadID}
	case "checkingGoal":
		kind, method = "goalGet", "thread/goal/get"
		params = map[string]any{"threadId": m.job.ThreadID}
	case "resumingGoal":
		kind, method = "goalResume", "thread/goal/set"
		params = map[string]any{"threadId": m.job.ThreadID, "status": "active"}
	default:
		m.mu.Unlock()
		return nil
	}
	if busy() {
		if kind == "turnStart" {
			m.job.Status = "pending"
		}
		m.touchLocked()
		_ = m.saveLocked()
		m.mu.Unlock()
		return nil
	}
	id := m.nextRequestLocked(kind)
	m.touchLocked()
	err := m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		m.fail("could not save crunch state: " + err.Error())
		return err
	}
	return m.writeRequest(id, method, params, input, inputMu, busy)
}

func (m *chatCrunchManager) hasPending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.job != nil && crunchJobActive(m.job.Status)
}

func (m *chatCrunchManager) running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return false
	}
	switch m.job.Status {
	case "running", "checkingGoal", "resumingGoal", "stopping":
		return true
	default:
		return false
	}
}

func (m *chatCrunchManager) fail(message string) {
	m.mu.Lock()
	m.failLocked(message)
	m.touchLocked()
	_ = m.saveLocked()
	m.mu.Unlock()
}

func (m *chatCrunchManager) failLocked(message string) {
	if m.job != nil {
		m.job.Status = "failed"
		m.job.Error = chatText(message)
		m.job.TurnID = ""
	}
	m.requestID, m.requestKind = "", ""
}

// usageLimitedLocked continues once on the fallback model, which may belong
// to the other provider, and otherwise waits for the usage reset. It returns
// the follow-up request to send.
func (m *chatCrunchManager) usageLimitedLocked(message string) string {
	if m.job.Fallback == "" || m.job.FellBack {
		m.waitForUsageLocked(message)
		return "rateLimits"
	}
	m.job.FellBack = true
	m.job.Error = ""
	m.job.TurnID = ""
	m.requestID, m.requestKind = "", ""
	if m.job.Mode == "goal" {
		m.job.Status = "resumingGoal"
		return "goalResume"
	}
	m.job.Status = "pending"
	return ""
}

func (m *chatCrunchManager) waitForUsageLocked(message string) {
	m.job.Status = "waitingUsage"
	m.job.Error = chatText(message)
	m.job.TurnID = ""
	m.job.ResetAt = time.Now()
	m.requestID, m.requestKind = "", ""
}

func (m *chatCrunchManager) nextRequestLocked(kind string) string {
	m.sequence++
	m.requestID = fmt.Sprintf("jara-crunch-%s-%d", m.job.ID, m.sequence)
	m.requestKind = kind
	return m.requestID
}

func (m *chatCrunchManager) writeRequest(id, method string, params any, input io.Writer, inputMu *sync.Mutex, busy func() bool) error {
	line, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		m.fail("could not encode crunch request: " + err.Error())
		return err
	}
	inputMu.Lock()
	if busy() {
		inputMu.Unlock()
		m.deferRequest(id)
		return nil
	}
	_, err = input.Write(append(line, '\n'))
	inputMu.Unlock()
	if err != nil {
		m.fail("could not send crunch request: " + err.Error())
		return err
	}
	return nil
}

func (m *chatCrunchManager) deferRequest(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.requestID {
		return
	}
	kind := m.requestKind
	m.requestID, m.requestKind = "", ""
	if kind == "turnStart" {
		m.job.Status = "pending"
	}
	if kind == "rateLimits" {
		m.job.ResetAt = time.Now().Add(time.Minute)
	}
	m.touchLocked()
	_ = m.saveLocked()
}

func (m *chatCrunchManager) touchLocked() {
	if m.job != nil {
		m.job.UpdatedAt = time.Now()
	}
}

func (m *chatCrunchManager) saveLocked() error {
	if m.job == nil {
		return os.Remove(m.path)
	}
	data, err := json.MarshalIndent(m.job, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(m.path), ".chat-crunch-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tempPath, m.path)
}

func crunchJobActive(status string) bool {
	switch status {
	case "pending", "running", "waitingUsage", "checkingGoal", "resumingGoal", "stopping":
		return true
	default:
		return false
	}
}

// crunchGoalName keeps Codex's wording, which cues its goal tool; Claude Code
// conversations complete goals through the router's GOAL COMPLETE marker.
func crunchGoalName(job chatCrunchJob) string {
	if thread := agentrouter.LoadThread(job.ThreadID); thread != nil && thread.Current().Agent == protocol.AgentClaude {
		return "goal"
	}
	return "Codex goal"
}

func crunchContinuationPrompt(job chatCrunchJob) string {
	if job.Mode == "goal" {
		return "Continue the active goal in this conversation from the current workspace state. Review the prior work and avoid repeating completed steps. When the objective is complete, mark the " + crunchGoalName(job) + " complete and stop: " + job.Prompt
	}
	return "Continue the task from where you stopped due to a usage limit. Review the conversation and current workspace state, avoid repeating completed work, and stop once this original task is complete: " + job.Prompt
}

func crunchInitialPrompt(job chatCrunchJob) string {
	if job.Mode == "goal" {
		return "Work toward the active " + crunchGoalName(job) + " in this conversation. When its objective is complete, mark the goal complete and stop: " + job.Prompt
	}
	return "Complete this task and stop when it is done. If usage limits interrupt the work, Jara will resume it after the usage window resets: " + job.Prompt
}

func writeCrunchMessage(input io.Writer, inputMu *sync.Mutex, message any) error {
	line, err := json.Marshal(message)
	if err != nil {
		return err
	}
	inputMu.Lock()
	defer inputMu.Unlock()
	_, err = input.Write(append(line, '\n'))
	return err
}

func quoteCrunchID(id string) string {
	quoted, _ := json.Marshal(id)
	return string(quoted)
}
