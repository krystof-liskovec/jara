package jara

import (
	"fmt"
	"strings"
	"time"
)

func chatTerminalActivity(u *chatUI, now time.Time) (string, string) {
	if u == nil || !u.busy {
		return "", ""
	}
	label := strings.TrimSpace(u.activityLabel)
	if label == "" {
		switch u.currentState() {
		case "starting", "resuming":
			label = "Opening conversation"
		case "waiting":
			label = "Waiting for input"
		default:
			label = "Thinking"
		}
	}
	if label == "Thinking" || label == "Planning" {
		if summary := chatActivitySummaryLabel(u.activitySummaryText); summary != "" {
			label = summary
		}
	}
	elapsed := ""
	if !u.turnStartedAt.IsZero() {
		elapsed = chatElapsedLabel(now.Sub(u.turnStartedAt))
	}
	return label, elapsed
}

// chatElapsedLabel formats a duration like Claude Code: "12s", "3m 4s", "1h 2m".
func chatElapsedLabel(d time.Duration) string {
	seconds := int(d.Round(time.Second) / time.Second)
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", max(0, seconds))
	case seconds < 3600:
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	default:
		return fmt.Sprintf("%dh %dm", seconds/3600, seconds%3600/60)
	}
}

func chatActivityHint(u *chatUI) string {
	if u.agentTurnActive() {
		return "ctrl+c to interrupt"
	}
	return ""
}
