package jara

import (
	"encoding/json"
	"fmt"
	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/protocol"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// usageReport is one host's usage limits: Codex's as account/rateLimits/read
// returns them and Claude Code's newest report on the host.
type usageReport struct {
	Codex       json.RawMessage              `json:"codex,omitempty"`
	CodexError  string                       `json:"codexError,omitempty"`
	Claude      json.RawMessage              `json:"claude,omitempty"` // {rateLimits, observedAt}
	ClaudeError string                       `json:"claudeError,omitempty"`
	Accounts    []agentrouter.AccountSummary `json:"accounts,omitempty"` // the pool, when it has several
}

type usageWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int64   `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type usageBucket struct {
	LimitID   string          `json:"limitId"`
	LimitName string          `json:"limitName"`
	PlanType  string          `json:"planType"`
	Primary   *usageWindow    `json:"primary"`
	Secondary *usageWindow    `json:"secondary"`
	Reached   json.RawMessage `json:"rateLimitReachedType"`
	Credits   *struct {
		HasCredits bool    `json:"hasCredits"`
		Unlimited  bool    `json:"unlimited"`
		Balance    *string `json:"balance"`
	} `json:"credits"`
}

const usageBarWidth = 20

// formatUsage renders a report as aligned lines for a terminal.
func formatUsage(report usageReport, now time.Time) string {
	var b strings.Builder
	switch {
	case report.CodexError != "":
		b.WriteString("Codex · " + report.CodexError + "\n")
	case len(report.Codex) > 0:
		writeCodexUsage(&b, report.Codex, now)
	}
	switch {
	case report.ClaudeError != "":
		b.WriteString("Claude Code · " + report.ClaudeError + "\n")
	case len(report.Claude) > 0:
		writeClaudeUsage(&b, report.Claude, now)
	}
	if b.Len() == 0 {
		b.WriteString("Neither Codex nor Claude Code is installed\n")
	}
	writeUsagePool(&b, report.Accounts, now)
	return b.String()
}

func writeCodexUsage(b *strings.Builder, raw json.RawMessage, now time.Time) {
	var response struct {
		RateLimits *usageBucket           `json:"rateLimits"`
		ByLimitID  map[string]usageBucket `json:"rateLimitsByLimitId"`
	}
	_ = json.Unmarshal(raw, &response)
	if response.RateLimits == nil {
		b.WriteString("Codex · no usage limits reported\n")
		return
	}
	main := *response.RateLimits
	title := "Codex"
	if plan := main.PlanType; plan != "" {
		title += " · " + strings.ToUpper(plan[:1]) + plan[1:]
	}
	b.WriteString(title + "\n")
	writeUsageBucket(b, main, "", now)
	for _, id := range slices.Sorted(maps.Keys(response.ByLimitID)) {
		bucket := response.ByLimitID[id]
		if id == main.LimitID || bucket.Primary == nil && bucket.Secondary == nil {
			continue
		}
		name := bucket.LimitName
		if name == "" {
			name = id
		}
		writeUsageBucket(b, bucket, name+" ", now)
	}
	if credits := main.Credits; credits != nil && (credits.Unlimited || credits.HasCredits) {
		balance := "unlimited"
		if !credits.Unlimited && credits.Balance != nil {
			balance = usageCredits(*credits.Balance)
		}
		b.WriteString("  Credits   " + balance + "\n")
	}
}

func writeClaudeUsage(b *strings.Builder, raw json.RawMessage, now time.Time) {
	var response struct {
		RateLimits *usageBucket `json:"rateLimits"`
		ObservedAt int64        `json:"observedAt"`
	}
	_ = json.Unmarshal(raw, &response)
	if response.RateLimits == nil || response.RateLimits.Primary == nil && response.RateLimits.Secondary == nil {
		b.WriteString("Claude Code · nothing reported yet; Claude Code reports usage during a turn\n")
		return
	}
	b.WriteString("Claude Code · reported " + usageAgo(now.Sub(time.Unix(response.ObservedAt, 0))) + "\n")
	writeUsageBucket(b, *response.RateLimits, "", now)
}

func writeUsageBucket(b *strings.Builder, bucket usageBucket, prefix string, now time.Time) {
	reached := !crunchRateLimitIsNull(bucket.Reached)
	for _, window := range []*usageWindow{bucket.Primary, bucket.Secondary} {
		if window == nil {
			continue
		}
		used := min(max(window.UsedPercent, 0), 100)
		reset := ""
		switch {
		case window.ResetsAt == 0:
		case window.ResetsAt <= now.Unix():
			reset, used = " · reset since "+usageWhen(time.Unix(window.ResetsAt, 0), now), 0
		default:
			reset = " · resets " + usageWhen(time.Unix(window.ResetsAt, 0), now)
		}
		if reached && used >= 100 {
			reset += " · limit reached"
		}
		filled := int(used/100*usageBarWidth + 0.5)
		bar := strings.Repeat("█", filled) + strings.Repeat("░", usageBarWidth-filled)
		fmt.Fprintf(b, "  %-9s %s %3.0f%%%s\n", prefix+usageWindowName(window.WindowDurationMins), bar, used, reset)
	}
}

func usageWindowName(minutes int64) string {
	switch {
	case minutes == 7*24*60:
		return "Weekly"
	case minutes == 30*24*60:
		return "Monthly"
	case minutes > 0 && minutes%(24*60) == 0:
		return fmt.Sprintf("%d-day", minutes/(24*60))
	case minutes > 0 && minutes%60 == 0:
		return fmt.Sprintf("%d-hour", minutes/60)
	case minutes > 0:
		return fmt.Sprintf("%d-min", minutes)
	}
	return "Limit"
}

// usageWhen names a time near now briefly: 17:20, Mon 09:00 or 2 Jan 09:00.
func usageWhen(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	switch {
	case at.YearDay() == now.YearDay() && at.Year() == now.Year():
		return at.Format("15:04")
	case at.Sub(now).Abs() < 7*24*time.Hour:
		return at.Format("Mon 15:04")
	}
	return at.Format("2 Jan 15:04")
}

func usageAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// usageCredits shortens a balance such as 61428.4375680000 to 61428.44.
func usageCredits(balance string) string {
	value, err := strconv.ParseFloat(balance, 64)
	if err != nil {
		return balance
	}
	return strconv.FormatFloat(math.Round(value*100)/100, 'f', -1, 64)
}

// writeUsagePool lists an agent's pooled accounts when it has several; the
// limits above are the account in use.
func writeUsagePool(b *strings.Builder, accounts []agentrouter.AccountSummary, now time.Time) {
	for _, agent := range []string{protocol.AgentCodex, protocol.AgentClaude} {
		var names []string
		for _, account := range accounts {
			if account.Agent != agent {
				continue
			}
			name := account.Label
			switch {
			case account.Active:
				name += " (in use)"
			case account.ExhaustedUntil > now.Unix():
				name += " (out until " + usageWhen(time.Unix(account.ExhaustedUntil, 0), now) + ")"
			}
			names = append(names, name)
		}
		if len(names) > 1 {
			title := map[string]string{protocol.AgentCodex: "Codex", protocol.AgentClaude: "Claude Code"}[agent]
			b.WriteString(title + " accounts: " + strings.Join(names, ", ") + "\n")
		}
	}
}
