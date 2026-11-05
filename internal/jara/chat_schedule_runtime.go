package jara

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const chatScheduleLimit = 50

type chatSchedule struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"threadId"`
	Prompt    string    `json:"prompt"`
	RunAt     time.Time `json:"runAt"`
	CreatedAt time.Time `json:"createdAt"`
	Status    string    `json:"status"`
	TurnID    string    `json:"turnId,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type chatScheduleManager struct {
	mu        sync.Mutex
	path      string
	jobs      []chatSchedule
	activeID  string
	requestID string
}

func newChatScheduleManager(path string) (*chatScheduleManager, error) {
	m := &chatScheduleManager{path: path}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("schedule store is not a regular file")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		if err = json.Unmarshal(data, &m.jobs); err != nil {
			return nil, fmt.Errorf("read scheduled prompts: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	changed := false
	for i := range m.jobs {
		if m.jobs[i].Status == "running" {
			m.jobs[i].Status = "failed"
			m.jobs[i].Error = "Jara restarted while this prompt was running; check the conversation before scheduling it again."
			m.jobs[i].TurnID = ""
			changed = true
		}
	}
	if changed {
		if err := m.saveLocked(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *chatScheduleManager) handleClientRequest(method string, params json.RawMessage, currentThread string) (any, error) {
	switch method {
	case "jara/schedule/create":
		var request struct {
			ThreadID string    `json:"threadId"`
			Prompt   string    `json:"prompt"`
			RunAt    time.Time `json:"runAt"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, fmt.Errorf("invalid schedule request: %w", err)
		}
		if request.ThreadID == "" || request.ThreadID != currentThread {
			return nil, fmt.Errorf("the conversation changed; retry /schedule in this conversation")
		}
		job, err := m.add(request.ThreadID, request.Prompt, request.RunAt)
		if err != nil {
			return nil, err
		}
		return map[string]any{"schedule": job}, nil
	case "jara/schedule/list":
		return map[string]any{"schedules": m.list(currentThread)}, nil
	case "jara/schedule/cancel":
		var request struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, fmt.Errorf("invalid schedule cancellation: %w", err)
		}
		job, err := m.cancel(request.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"schedule": job}, nil
	default:
		return nil, fmt.Errorf("unsupported Jara schedule operation")
	}
}

func (m *chatScheduleManager) add(threadID, prompt string, runAt time.Time) (chatSchedule, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return chatSchedule{}, fmt.Errorf("scheduled prompt cannot be empty")
	}
	if len(prompt) > maxBackgroundPrompt {
		return chatSchedule{}, fmt.Errorf("scheduled prompt exceeds 64 KiB")
	}
	now := time.Now()
	if !runAt.After(now) || runAt.After(now.AddDate(1, 0, 0)) {
		return chatSchedule{}, fmt.Errorf("schedule time must be in the future and within one year")
	}
	bytes := make([]byte, 5)
	if _, err := rand.Read(bytes); err != nil {
		return chatSchedule{}, err
	}
	job := chatSchedule{
		ID: "sc-" + hex.EncodeToString(bytes), ThreadID: threadID,
		Prompt: prompt, RunAt: runAt, CreatedAt: now, Status: "pending",
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := 0
	for _, existing := range m.jobs {
		if existing.Status == "pending" {
			pending++
		}
	}
	if pending >= chatScheduleLimit {
		return chatSchedule{}, fmt.Errorf("this workspace already has %d pending scheduled prompts", chatScheduleLimit)
	}
	m.jobs = append(m.jobs, job)
	if err := m.saveLocked(); err != nil {
		m.jobs = m.jobs[:len(m.jobs)-1]
		return chatSchedule{}, err
	}
	return job, nil
}

func (m *chatScheduleManager) list(threadID string) []chatSchedule {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jobs []chatSchedule
	for _, job := range m.jobs {
		if threadID == "" || job.ThreadID == threadID {
			jobs = append(jobs, job)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].RunAt.Before(jobs[j].RunAt) })
	return jobs
}

func (m *chatScheduleManager) cancel(id string) (chatSchedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.jobs {
		if m.jobs[i].ID != id {
			continue
		}
		if m.jobs[i].Status != "pending" {
			return chatSchedule{}, fmt.Errorf("schedule %s is %s and cannot be canceled", id, m.jobs[i].Status)
		}
		m.jobs[i].Status = "canceled"
		if err := m.saveLocked(); err != nil {
			m.jobs[i].Status = "pending"
			return chatSchedule{}, err
		}
		return m.jobs[i], nil
	}
	return chatSchedule{}, fmt.Errorf("no scheduled prompt has ID %s", id)
}

func (m *chatScheduleManager) cancelPending() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for i := range m.jobs {
		if m.jobs[i].Status == "pending" {
			m.jobs[i].Status = "canceled"
			changed = true
		}
	}
	if changed {
		return m.saveLocked()
	}
	return nil
}

func (m *chatScheduleManager) hasPending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.Status == "pending" || job.Status == "running" {
			return true
		}
	}
	return false
}

func (m *chatScheduleManager) running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeID != ""
}

func (m *chatScheduleManager) startDue(input io.Writer, inputMu *sync.Mutex, busy func() bool) error {
	if busy() {
		return nil
	}
	m.mu.Lock()
	if m.activeID != "" {
		m.mu.Unlock()
		return nil
	}
	now := time.Now()
	index := -1
	for i := range m.jobs {
		if m.jobs[i].Status == "pending" && !m.jobs[i].RunAt.After(now) && (index < 0 || m.jobs[i].RunAt.Before(m.jobs[index].RunAt)) {
			index = i
		}
	}
	if index < 0 {
		m.mu.Unlock()
		return nil
	}
	job := &m.jobs[index]
	job.Status = "running"
	job.Error = ""
	m.activeID = job.ID
	m.requestID = "jara-schedule-start-" + job.ID
	message, err := json.Marshal(map[string]any{
		"id": m.requestID, "method": "turn/start",
		"params": map[string]any{
			"threadId": job.ThreadID,
			"input":    []any{map[string]any{"type": "text", "text": job.Prompt}},
			"summary":  "concise",
		},
	})
	if err == nil {
		err = m.saveLocked()
	}
	requestID := m.requestID
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		m.activeID, m.requestID = "", ""
		_ = m.saveLocked()
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	inputMu.Lock()
	if busy() {
		inputMu.Unlock()
		return m.deferStart(requestID)
	}
	_, err = input.Write(append(message, '\n'))
	inputMu.Unlock()
	if err != nil {
		m.finishFailed(requestID, err.Error())
	}
	return err
}

func (m *chatScheduleManager) deferStart(requestID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if requestID != m.requestID {
		return nil
	}
	if job := m.activeJobLocked(); job != nil {
		job.Status = "pending"
		job.Error = ""
	}
	m.activeID, m.requestID = "", ""
	return m.saveLocked()
}

func (m *chatScheduleManager) handleAppMessage(message appMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeID == "" {
		return
	}
	if len(message.ID) > 0 && message.Method == "" && string(message.ID) == fmt.Sprintf("%q", m.requestID) {
		job := m.activeJobLocked()
		if job == nil {
			return
		}
		if message.Error != nil {
			job.Status = "failed"
			job.Error = chatText(message.Error.Message)
			m.activeID, m.requestID = "", ""
			_ = m.saveLocked()
			return
		}
		var response struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(message.Result, &response) != nil || response.Turn.ID == "" {
			job.Status = "failed"
			job.Error = "Codex did not return a turn ID"
			m.activeID, m.requestID = "", ""
			_ = m.saveLocked()
			return
		}
		job.TurnID = response.Turn.ID
		_ = m.saveLocked()
		return
	}
	if message.Method != "turn/completed" {
		return
	}
	var event struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(message.Params, &event) != nil {
		return
	}
	job := m.activeJobLocked()
	if job == nil || job.TurnID == "" || job.TurnID != event.Turn.ID {
		return
	}
	if event.Turn.Status == "completed" {
		job.Status = "completed"
	} else {
		job.Status = "failed"
		job.Error = "scheduled turn ended with status " + event.Turn.Status
	}
	job.TurnID = ""
	m.activeID, m.requestID = "", ""
	_ = m.saveLocked()
}

func (m *chatScheduleManager) activeJobLocked() *chatSchedule {
	for i := range m.jobs {
		if m.jobs[i].ID == m.activeID {
			return &m.jobs[i]
		}
	}
	return nil
}

func (m *chatScheduleManager) finishFailed(requestID, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if requestID != m.requestID {
		return
	}
	if job := m.activeJobLocked(); job != nil {
		job.Status = "failed"
		job.Error = chatText(detail)
	}
	m.activeID, m.requestID = "", ""
	_ = m.saveLocked()
}

func (m *chatScheduleManager) saveLocked() error {
	data, err := json.MarshalIndent(m.jobs, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(m.path), ".chat-schedules-*")
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
