package jara

import (
	"os"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
)

// Alerts ring the terminal bell and post a desktop notification (OSC 9) when
// a long turn ends or the agent waits for the user. Inside tmux the bell also
// flags the window, and the notification is passed through to the outer
// terminal when tmux allows it.

const chatAlertAfter = 15 * time.Second

func (u *chatUI) alertUser(message string) {
	if u.alert == nil || u.alertsOff {
		return
	}
	u.alert(chatAlertSequence("Jara · "+textutil.FirstNonEmpty(u.workspaceLabel, "conversation"), message, os.Getenv("TMUX") != ""))
}

// alertTurnEnded alerts once a turn that ran for a while finishes; quick
// replies the user is watching need no alert.
func (u *chatUI) alertTurnEnded(status string) {
	if u.turnStartedAt.IsZero() || time.Since(u.turnStartedAt) < chatAlertAfter {
		return
	}
	switch status {
	case "completed":
		u.alertUser("Turn finished")
	case "interrupted":
	default:
		u.alertUser("Turn ended: " + status)
	}
}

func chatAlertSequence(title, body string, tmux bool) string {
	clean := func(text string) string {
		return strings.Map(func(r rune) rune {
			if r < 32 || r == 127 || r >= 128 && r <= 159 || r == ';' {
				return ' '
			}
			return r
		}, text)
	}
	notification := "\x1b]9;" + clean(title) + ": " + clean(body) + "\x07"
	if tmux {
		notification = "\x1bPtmux;" + strings.ReplaceAll(notification, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return "\a" + notification
}

func (u *chatUI) runChatAlertsCommand(args string) {
	switch strings.ToLower(args) {
	case "on":
		u.alertsOff = false
	case "off":
		u.alertsOff = true
	case "", "status":
	default:
		u.print("Use /alerts on, /alerts off, or /alerts status.\n")
		return
	}
	state := "on: the terminal bell and a desktop notification mark long turns that finish and requests that need you"
	if u.alertsOff {
		state = "off"
	}
	u.printRole("system", "Alerts are "+state+".\n")
}
