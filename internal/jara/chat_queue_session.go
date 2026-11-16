package jara

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// A persistent session keeps its queued prompts as scheduled jobs that are
// due at once (Queued). The session runs them one at a time whenever the
// conversation is idle, also without a window, and they move with the
// conversation to whichever machine continues it. Windows show and edit them
// and receive jara/queue/updated after every change.

func chatQueueRequest(method string) bool {
	switch method {
	case "jara/queue/add", "jara/queue/update", "jara/queue/remove", "jara/queue/clear":
		return true
	}
	return false
}

func (m *chatScheduleManager) handleQueueRequest(method string, params json.RawMessage, currentThread string) (any, error) {
	var request struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
		Prompt   string `json:"prompt"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, fmt.Errorf("invalid queue request: %w", err)
	}
	var err error
	switch method {
	case "jara/queue/add":
		if request.ThreadID == "" || request.ThreadID != currentThread {
			return nil, fmt.Errorf("the conversation changed; queue the prompt again")
		}
		err = m.enqueue(request.ThreadID, request.Prompt)
	case "jara/queue/update":
		err = m.changeQueued(request.ID, func(job *chatSchedule) error {
			prompt := strings.TrimSpace(request.Prompt)
			if prompt == "" {
				return fmt.Errorf("queued prompts cannot be empty")
			}
			job.Prompt = prompt
			return nil
		})
	case "jara/queue/remove":
		err = m.changeQueued(request.ID, func(job *chatSchedule) error { job.Status = "cancelled"; return nil })
	case "jara/queue/clear":
		m.mu.Lock()
		for i := range m.jobs {
			if m.jobs[i].Queued && m.jobs[i].Status == "pending" && m.jobs[i].ThreadID == request.ThreadID {
				m.jobs[i].Status = "cancelled"
			}
		}
		err = m.saveLocked()
		m.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	m.notifyQueue()
	return map[string]any{}, nil
}

func (m *chatScheduleManager) enqueue(threadID, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("enter a prompt before queuing it")
	}
	if len(prompt) > maxBackgroundPrompt {
		return fmt.Errorf("queued prompt exceeds 64 KiB")
	}
	id := make([]byte, 5)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	queued := 0
	for _, job := range m.jobs {
		if job.Queued && job.Status == "pending" {
			queued++
		}
	}
	if queued >= chatPromptQueueLimit {
		return fmt.Errorf("prompt queue is full (%d prompts)", chatPromptQueueLimit)
	}
	now := time.Now()
	m.jobs = append(m.jobs, chatSchedule{ID: "q-" + hex.EncodeToString(id), ThreadID: threadID, Prompt: prompt,
		RunAt: now, CreatedAt: now, Status: "pending", Queued: true})
	return m.saveLocked()
}

func (m *chatScheduleManager) changeQueued(id string, change func(*chatSchedule) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.jobs {
		if m.jobs[i].ID == id && m.jobs[i].Queued {
			if m.jobs[i].Status != "pending" {
				return fmt.Errorf("that queued prompt already started")
			}
			if err := change(&m.jobs[i]); err != nil {
				return err
			}
			return m.saveLocked()
		}
	}
	return fmt.Errorf("that queued prompt already started")
}

// queuedPrompts lists pending queued prompts in the order they run.
func (m *chatScheduleManager) queuedPrompts() []chatSchedule {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jobs []chatSchedule
	for _, job := range m.jobs {
		if job.Queued && job.Status == "pending" {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

func (m *chatScheduleManager) notifyQueue() {
	if m.onQueue != nil {
		m.onQueue(chatQueueNotification(m.queuedPrompts()))
	}
}

func chatQueueNotification(jobs []chatSchedule) []byte {
	prompts := make([]map[string]string, 0, len(jobs))
	for _, job := range jobs {
		prompts = append(prompts, map[string]string{"id": job.ID, "threadId": job.ThreadID, "prompt": job.Prompt})
	}
	line, _ := json.Marshal(map[string]any{"method": "jara/queue/updated", "params": map[string]any{"prompts": prompts}})
	return line
}
