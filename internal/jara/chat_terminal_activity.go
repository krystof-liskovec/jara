package jara

import (
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
		elapsed = now.Sub(u.turnStartedAt).Round(time.Second).String()
	}
	return label, elapsed
}
