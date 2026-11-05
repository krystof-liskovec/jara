package jara

import (
	"fmt"
	"io"
	"sync"
)

func (m *chatCrunchManager) stop(input io.Writer, inputMu *sync.Mutex, currentThread string) error {
	m.mu.Lock()
	if m.job == nil || !crunchJobActive(m.job.Status) {
		m.mu.Unlock()
		return fmt.Errorf("no crunch task is running")
	}
	if currentThread != "" && m.job.ThreadID != currentThread {
		m.mu.Unlock()
		return fmt.Errorf("the crunch task belongs to another conversation")
	}
	mode, threadID, turnID := m.job.Mode, m.job.ThreadID, m.job.TurnID
	if m.job.Status == "running" {
		m.job.Status = "stopping"
	} else {
		m.job.Status = "stopped"
		m.requestID, m.requestKind = "", ""
	}
	m.job.Error = ""
	m.touchLocked()
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.sequence++
	interruptID := fmt.Sprintf("jara-crunch-%s-%d", m.job.ID, m.sequence)
	m.sequence++
	goalID := fmt.Sprintf("jara-crunch-%s-%d", m.job.ID, m.sequence)
	m.mu.Unlock()
	if mode == "goal" {
		if err := writeCrunchMessage(input, inputMu, map[string]any{
			"id": goalID, "method": "thread/goal/set", "params": map[string]string{"threadId": threadID, "status": "paused"},
		}); err != nil {
			return err
		}
	}
	if turnID != "" {
		return writeCrunchMessage(input, inputMu, map[string]any{
			"id": interruptID, "method": "turn/interrupt", "params": map[string]string{"threadId": threadID, "turnId": turnID},
		})
	}
	return nil
}

func (m *chatCrunchManager) stopAll(input io.Writer, inputMu *sync.Mutex) {
	m.mu.Lock()
	if m.job == nil || !crunchJobActive(m.job.Status) {
		m.mu.Unlock()
		return
	}
	mode, threadID, turnID := m.job.Mode, m.job.ThreadID, m.job.TurnID
	m.job.Status = "stopped"
	m.job.Error = ""
	m.requestID, m.requestKind = "", ""
	m.touchLocked()
	_ = m.saveLocked()
	m.sequence++
	interruptID := fmt.Sprintf("jara-crunch-%s-%d", m.job.ID, m.sequence)
	m.sequence++
	goalID := fmt.Sprintf("jara-crunch-%s-%d", m.job.ID, m.sequence)
	m.mu.Unlock()
	if mode == "goal" {
		_ = writeCrunchMessage(input, inputMu, map[string]any{
			"id": goalID, "method": "thread/goal/set", "params": map[string]string{"threadId": threadID, "status": "paused"},
		})
	}
	if turnID != "" {
		_ = writeCrunchMessage(input, inputMu, map[string]any{
			"id": interruptID, "method": "turn/interrupt", "params": map[string]string{"threadId": threadID, "turnId": turnID},
		})
	}
}
