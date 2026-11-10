package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

func (m *chatCrunchManager) handleAppMessage(message appMessage, input io.Writer, inputMu *sync.Mutex, busy func() bool) {
	var followUp string
	m.mu.Lock()
	if m.job == nil {
		m.mu.Unlock()
		return
	}
	if len(message.ID) > 0 && message.Method == "" && string(message.ID) == quoteCrunchID(m.requestID) {
		kind := m.requestKind
		m.requestID, m.requestKind = "", ""
		if message.Error != nil {
			if kind == "turnStart" && chatCrunchIsUsageLimit(message.Error.Data, message.Error.Message) {
				m.waitForUsageLocked(message.Error.Message)
				followUp = "rateLimits"
			} else if kind == "turnInterrupt" {
				m.job.Status = "stopped"
				m.job.Error = ""
			} else {
				m.failLocked(kind + " failed: " + chatText(message.Error.Message))
			}
		} else {
			switch kind {
			case "turnStart":
				var response struct {
					Turn struct {
						ID string `json:"id"`
					} `json:"turn"`
				}
				if json.Unmarshal(message.Result, &response) != nil || response.Turn.ID == "" {
					m.failLocked("the agent did not return a turn ID")
				} else {
					m.job.TurnID = response.Turn.ID
					m.job.Started = true
					if m.job.Status == "stopping" {
						followUp = "turnInterrupt"
					}
				}
			case "rateLimits":
				blocked, next, err := chatCrunchRateLimitState(message.Result, time.Now())
				if err != nil {
					m.failLocked("could not read the usage reset time: " + err.Error())
				} else if blocked {
					m.job.ResetAt = next
					m.job.Status = "waitingUsage"
				} else {
					m.job.ResetAt = time.Time{}
					if m.job.Mode == "goal" {
						m.job.Status = "resumingGoal"
						followUp = "goalResume"
					} else {
						m.job.Status = "pending"
					}
				}
			case "goalGet":
				var response struct {
					Goal *struct {
						Status string `json:"status"`
					} `json:"goal"`
				}
				if json.Unmarshal(message.Result, &response) != nil || response.Goal == nil {
					m.failLocked("the conversation no longer has a goal for this goal crunch")
				} else {
					m.job.GoalStatus = response.Goal.Status
					switch response.Goal.Status {
					case "complete":
						m.job.Status = "completed"
					case "active":
						m.job.Status = "pending"
					case "usageLimited":
						m.waitForUsageLocked("the goal is usage-limited")
						followUp = "rateLimits"
					default:
						m.failLocked("goal status is " + chatGoalStatusLabel(response.Goal.Status))
					}
				}
			case "goalResume":
				m.job.GoalStatus = "active"
				m.job.Status = "pending"
			case "turnInterrupt":
				m.job.Status = "stopped"
				m.job.Error = ""
			}
		}
		m.touchLocked()
		_ = m.saveLocked()
	} else if message.Method == "turn/completed" {
		var event struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message        string          `json:"message"`
					CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(message.Params, &event) == nil {
			if m.job.TurnID == "" && m.requestKind == "turnStart" {
				m.job.TurnID = event.Turn.ID
			}
			if m.job.TurnID != "" && m.job.TurnID == event.Turn.ID {
				m.job.TurnID = ""
				m.requestID, m.requestKind = "", ""
				switch {
				case m.job.Status == "stopping" || event.Turn.Status == "interrupted":
					m.job.Status = "stopped"
					if m.job.Mode == "goal" {
						followUp = "goalPause"
					}
				case event.Turn.Status == "completed" && m.job.Mode == "goal":
					m.job.Status = "checkingGoal"
					followUp = "goalGet"
				case event.Turn.Status == "completed":
					m.job.Status = "completed"
				case event.Turn.Error != nil && chatCrunchIsUsageLimit(event.Turn.Error.CodexErrorInfo, event.Turn.Error.Message):
					m.waitForUsageLocked(event.Turn.Error.Message)
					followUp = "rateLimits"
				default:
					failure := "turn ended with status " + event.Turn.Status
					if event.Turn.Error != nil && event.Turn.Error.Message != "" {
						failure = chatText(event.Turn.Error.Message)
					}
					m.failLocked(failure)
				}
				m.touchLocked()
				_ = m.saveLocked()
			}
		}
	} else if message.Method == "account/rateLimits/updated" && m.job.Status == "waitingUsage" {
		m.job.ResetAt = time.Now()
		m.touchLocked()
		_ = m.saveLocked()
	}
	if message.Method == "thread/goal/updated" && m.job.Mode == "goal" {
		var update struct {
			ThreadID string `json:"threadId"`
			Goal     struct {
				Status string `json:"status"`
			} `json:"goal"`
		}
		if json.Unmarshal(message.Params, &update) == nil && update.ThreadID == m.job.ThreadID {
			m.job.GoalStatus = update.Goal.Status
			if update.Goal.Status == "complete" && m.job.Status != "running" {
				m.job.Status = "completed"
			}
			m.touchLocked()
			_ = m.saveLocked()
		}
	}
	m.mu.Unlock()
	if followUp != "" {
		_ = m.queueFollowUp(followUp, input, inputMu, busy)
	}
}

func (m *chatCrunchManager) queueFollowUp(kind string, input io.Writer, inputMu *sync.Mutex, busy func() bool) error {
	m.mu.Lock()
	if m.job == nil || m.requestID != "" {
		m.mu.Unlock()
		return nil
	}
	method := ""
	params := map[string]any{}
	switch kind {
	case "rateLimits":
		method = "account/rateLimits/read"
		params["threadId"] = m.job.ThreadID // the router answers per agent
	case "goalGet":
		method = "thread/goal/get"
		params["threadId"] = m.job.ThreadID
	case "goalResume":
		method = "thread/goal/set"
		params["threadId"] = m.job.ThreadID
		params["status"] = "active"
	case "turnInterrupt":
		method = "turn/interrupt"
		params["threadId"] = m.job.ThreadID
		params["turnId"] = m.job.TurnID
	case "goalPause":
		method = "thread/goal/set"
		params["threadId"] = m.job.ThreadID
		params["status"] = "paused"
	default:
		m.mu.Unlock()
		return fmt.Errorf("unknown crunch follow-up %q", kind)
	}
	if kind == "goalPause" || kind == "turnInterrupt" {
		m.sequence++
		id := fmt.Sprintf("jara-crunch-%s-%d", m.job.ID, m.sequence)
		m.mu.Unlock()
		return writeCrunchMessage(input, inputMu, map[string]any{"id": id, "method": method, "params": params})
	}
	id := m.nextRequestLocked(kind)
	if err := m.saveLocked(); err != nil {
		m.requestID, m.requestKind = "", ""
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	return m.writeRequest(id, method, params, input, inputMu, busy)
}
