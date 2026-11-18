package agentrouter

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// When a turn stops at the usage limit and the pool has an account with
// usage left, the router switches to it and the turn goes on: clients see one
// turn that continues, not a failure. Codex continues in a new turn, which
// the router presents under the original turn's ID; Claude Code restarts on
// the new account and resumes its session within the same turn.

// AccountContinuation is the message that continues a turn on the next
// account; histories leave it out.
const AccountContinuation = "Your previous message stopped at an account's usage limit and Jara switched to another account. Continue exactly where you left off."

type accountSwitches struct {
	held       map[string]bool   // Codex thread → its usage-limit error was held back
	continuing map[string]string // Codex thread → the turn its next turn continues
	alias      map[string]string // continuation turn → the turn clients know
	latest     map[string]string // the turn clients know → the one running now
	resetsAt   int64             // when the Codex account in use gets usage back
}

// accountAvailable reports whether the pool has another account with usage
// left for agent; it only reads the pool.
func accountAvailable(agent string) bool {
	current, _ := currentAccount(agent)
	for _, account := range loadAccountPool()[agent] {
		if account.ID != current.ID && account.ExhaustedUntil <= time.Now().Unix() {
			return true
		}
	}
	return false
}

func (r *agentRouter) notifyAccountSwitched(agent, thread string, from, to poolAccount, resetsAt int64) {
	r.notify("jara/account/switched", map[string]any{"agent": agent, "threadId": thread, "from": from.Label, "to": to.Label, "resetsAt": resetsAt})
}

// holdForAccountSwitch handles a Codex notification for the pool: it
// presents continuation turns under their original ID and reports whether
// the notification is held back.
func (r *agentRouter) holdForAccountSwitch(m *rpcMessage) bool {
	sw := &r.switches
	for continuation, original := range sw.alias {
		m.Params = bytes.ReplaceAll(m.Params, []byte(strconv.Quote(continuation)), []byte(strconv.Quote(original)))
	}
	var event struct {
		ThreadID  string `json:"threadId"`
		TurnID    string `json:"turnId"`
		WillRetry bool   `json:"willRetry"`
		Error     *struct {
			Message        string          `json:"message"`
			CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
		} `json:"error"`
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message        string          `json:"message"`
				CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		} `json:"turn"`
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	_ = json.Unmarshal(m.Params, &event)
	thread := event.ThreadID
	switch m.Method {
	case "account/rateLimits/updated":
		sw.resetsAt = exhaustedUntil(m.Params)
	case "account/login/completed", "account/updated":
		RememberAccounts()
	case "error":
		if !event.WillRetry && event.Error != nil && !r.codexChildren[thread] &&
			protocol.IsUsageLimit(event.Error.CodexErrorInfo, event.Error.Message) && accountAvailable(protocol.AgentCodex) {
			if sw.held == nil {
				sw.held = map[string]bool{}
			}
			sw.held[thread] = true
			return true
		}
	case "turn/started":
		if original, ok := sw.continuing[thread]; ok {
			delete(sw.continuing, thread)
			sw.mark(&sw.alias)[event.Turn.ID] = original
			sw.mark(&sw.latest)[original] = event.Turn.ID
			return true
		}
	case "item/started", "item/completed":
		return event.Item.Type == "userMessage" && bytes.Contains(m.Params, []byte(AccountContinuation))
	case "turn/completed":
		delete(sw.held, thread)
		failure := event.Turn.Error
		if event.Turn.Status == "failed" && failure != nil && !r.codexChildren[thread] &&
			protocol.IsUsageLimit(failure.CodexErrorInfo, failure.Message) && r.continueCodexTurn(thread, event.Turn.ID) {
			return true
		}
		sw.forget(event.Turn.ID)
	}
	return false
}

func (sw *accountSwitches) mark(m *map[string]string) map[string]string {
	if *m == nil {
		*m = map[string]string{}
	}
	return *m
}

// forget drops the continuations of a turn that ended.
func (sw *accountSwitches) forget(original string) {
	for continuation, known := range sw.alias {
		if known == original {
			delete(sw.alias, continuation)
		}
	}
	delete(sw.latest, original)
}

// continueCodexTurn switches the account and continues original on it.
func (r *agentRouter) continueCodexTurn(thread, original string) bool {
	from, to, ok := switchAccount(protocol.AgentCodex, r.switches.resetsAt)
	if !ok {
		return false
	}
	r.notifyAccountSwitched(protocol.AgentCodex, r.codexToVirtualOr(thread), from, to, r.switches.resetsAt)
	r.switches.resetsAt = 0
	r.switches.mark(&r.switches.continuing)[thread] = original
	params, _ := json.Marshal(map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": AccountContinuation}}})
	err := r.callCodex(codexCall{method: "turn/start", callback: func(_ json.RawMessage, failure json.RawMessage) {
		if len(failure) > 0 && string(failure) != "null" {
			r.endContinuation(thread, original, failure)
		}
	}}, params)
	if err != nil {
		r.endContinuation(thread, original, json.RawMessage(strconv.Quote(err.Error())))
	}
	return true
}

// endContinuation reports the turn as failed when its continuation could not start.
func (r *agentRouter) endContinuation(thread, original string, failure json.RawMessage) {
	delete(r.switches.continuing, thread)
	r.switches.forget(original)
	var detail struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(failure, &detail) != nil || detail.Message == "" {
		detail.Message = string(failure)
	}
	params, _ := json.Marshal(map[string]any{"threadId": thread, "turn": map[string]any{"id": original, "status": "failed",
		"error": map[string]any{"message": "Could not continue on the next account: " + detail.Message}}})
	r.send(rpcMessage{Method: "turn/completed", Params: r.rewriteCodexThread(params)})
}

func (r *agentRouter) codexToVirtualOr(thread string) string {
	if virtual := r.codexToVirtual[thread]; virtual != "" {
		return virtual
	}
	return thread
}

// realTurnIDs gives Codex the running continuation for a turn clients name.
func (r *agentRouter) realTurnIDs(params json.RawMessage) json.RawMessage {
	for original, running := range r.switches.latest {
		params = bytes.ReplaceAll(params, []byte(strconv.Quote(original)), []byte(strconv.Quote(running)))
	}
	return params
}

// exhaustedUntil reads when a Codex rate-limit report's full window resets.
func exhaustedUntil(params json.RawMessage) int64 {
	var report struct {
		RateLimits struct {
			Primary, Secondary *struct {
				UsedPercent float64 `json:"usedPercent"`
				ResetsAt    int64   `json:"resetsAt"`
			}
		} `json:"rateLimits"`
	}
	_ = json.Unmarshal(params, &report)
	var until int64
	for _, window := range []*struct {
		UsedPercent float64 `json:"usedPercent"`
		ResetsAt    int64   `json:"resetsAt"`
	}{report.RateLimits.Primary, report.RateLimits.Secondary} {
		if window != nil && window.UsedPercent >= 100 && window.ResetsAt > until {
			until = window.ResetsAt
		}
	}
	return until
}

// continueOnNextAccount switches Claude Code's account when a turn stops at
// the usage limit and resumes the session within the same turn.
func (s *claudeSession) continueOnNextAccount() bool {
	if s.turnID == "" || s.compacting {
		return false
	}
	var resetsAt int64
	if s.r.claudeLimits != nil {
		resetsAt = s.r.claudeLimits.ResetsAt
	}
	from, to, ok := switchAccount(protocol.AgentClaude, resetsAt)
	if !ok {
		return false
	}
	s.r.claudeLimits = nil // the report was about the previous account
	s.r.notifyAccountSwitched(protocol.AgentClaude, s.thread.ID, from, to, resetsAt)
	s.stop()
	if err := s.startProcess(); err != nil {
		s.finishTurn("failed", "Could not start Claude Code on the next account: "+err.Error())
		return true
	}
	message := map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": AccountContinuation}}}
	if err := s.write(map[string]any{"type": "user", "message": message}); err != nil {
		s.finishTurn("failed", "Could not continue on the next account: "+err.Error())
	}
	return true
}

// handleAccountRequest answers the interface's pool requests.
func (r *agentRouter) handleAccountRequest(id json.RawMessage, method string, params json.RawMessage) {
	var request struct{ Agent, ID string }
	_ = json.Unmarshal(params, &request)
	var err error
	switch method {
	case "jara/accounts/list":
		r.reply(id, map[string]any{"accounts": Accounts()})
		return
	case "jara/accounts/use":
		err = UseAccount(request.Agent, request.ID)
	case "jara/accounts/remove":
		err = RemoveAccount(request.Agent, request.ID)
	}
	if err != nil {
		r.fail(id, err.Error())
		return
	}
	r.reply(id, map[string]any{"accounts": Accounts()})
}
