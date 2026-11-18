package agentrouter

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// Claude Code reports subscription usage as rate_limit_event lines. The router
// keeps the latest report so crunch can wait for usage resets on Claude
// conversations, using the same account/rateLimits protocol as for Codex.
type claudeRateLimits struct {
	Status        string `json:"status"` // allowed, allowed_warning, rejected
	ResetsAt      int64  `json:"resetsAt"`
	RateLimitType string `json:"rateLimitType"`
	Windows       map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
		Status      string  `json:"status"`
	} `json:"unifiedWindows"`
}

var claudeLimitText = regexp.MustCompile(`(?i)usage limit|hit your limit|limit reached|rate.?limit`)
var claudeResetEpoch = regexp.MustCompile(`\|(\d{10})\b`)

// observeClaudeRateLimits records a rate_limit_event and announces when a
// rejected window becomes usable again.
func (r *agentRouter) observeClaudeRateLimits(raw json.RawMessage) {
	var info claudeRateLimits
	if json.Unmarshal(raw, &info) != nil || info.Status == "" {
		return
	}
	wasRejected := r.claudeLimits != nil && r.claudeLimits.Status == "rejected"
	r.claudeLimits = &info
	saveClaudeUsage(&info)
	if wasRejected && info.Status != "rejected" {
		r.notify("account/rateLimits/updated", map[string]any{"rateLimits": r.claudeRateLimitBucket()})
	}
}

// claudeUsageLimited reports whether a failed turn hit a subscription limit,
// and extends the known reset time from Claude's message when it carries one.
func (r *agentRouter) claudeUsageLimited(message string) bool {
	limited := r.claudeLimits != nil && r.claudeLimits.Status == "rejected" || claudeLimitText.MatchString(message)
	if !limited {
		return false
	}
	if r.claudeLimits == nil {
		r.claudeLimits = &claudeRateLimits{}
	}
	r.claudeLimits.Status = "rejected"
	if match := claudeResetEpoch.FindStringSubmatch(message); match != nil {
		if reset, err := strconv.ParseInt(match[1], 10, 64); err == nil && reset > r.claudeLimits.ResetsAt {
			r.claudeLimits.ResetsAt = reset
		}
	}
	saveClaudeUsage(r.claudeLimits)
	return true
}

// claudeRateLimitBucket converts Claude's windows into Codex's rate-limit
// shape: the five-hour window as primary and the weekly one as secondary.
func (r *agentRouter) claudeRateLimitBucket() map[string]any {
	return claudeLimitBucket(r.claudeLimits)
}

func claudeLimitBucket(info *claudeRateLimits) map[string]any {
	if info == nil {
		return map[string]any{"primary": nil, "secondary": nil, "rateLimitReachedType": nil}
	}
	window := func(names []string, minutes int64) map[string]any {
		for _, name := range names {
			if w, ok := info.Windows[name]; ok {
				used := w.Utilization * 100
				if w.Status == "rejected" {
					used = 100
				}
				return map[string]any{"usedPercent": used, "windowDurationMins": minutes, "resetsAt": w.ResetsAt}
			}
		}
		return nil
	}
	primary := window([]string{"five_hour"}, 300)
	secondary := window([]string{"seven_day", "seven_day_opus", "seven_day_sonnet"}, 7*24*60)
	var reached any
	if info.Status == "rejected" {
		reset := info.ResetsAt
		if reset <= time.Now().Unix() {
			reset = time.Now().Add(15 * time.Minute).Unix() // unknown reset: check again later
		}
		reached = "primary"
		if strings.HasPrefix(info.RateLimitType, "seven_day") {
			reached = "secondary"
			secondary = map[string]any{"usedPercent": 100, "windowDurationMins": 7 * 24 * 60, "resetsAt": reset}
		} else {
			primary = map[string]any{"usedPercent": 100, "windowDurationMins": 300, "resetsAt": reset}
		}
	}
	return map[string]any{"primary": primary, "secondary": secondary, "rateLimitReachedType": reached}
}

// readRateLimits answers crunch's usage query. Requests for a Claude
// conversation get Claude's windows; all others go to Codex unchanged.
func (r *agentRouter) readRateLimits(id json.RawMessage, params json.RawMessage) error {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(params, &fields)
	var threadID string
	_ = json.Unmarshal(fields["threadId"], &threadID)
	delete(fields, "threadId")
	if thread := r.threads[threadID]; thread != nil && thread.Current().Agent == protocol.AgentClaude {
		r.reply(id, map[string]any{"rateLimits": r.claudeRateLimitBucket()})
		return nil
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	stripped, _ := json.Marshal(fields)
	return r.callCodex(codexCall{uiID: id, method: "account/rateLimits/read"}, stripped)
}
