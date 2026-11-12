package jara

import (
	"encoding/json"
	"strings"
)

// /notify sets where the session daemon sends push notifications.

func (u *chatUI) runChatNotifyCommand(args string) error {
	method, params := "jara/notify/get", map[string]string{}
	switch strings.ToLower(args) {
	case "", "status":
	case "off":
		method, params["url"] = "jara/notify/set", ""
	case "test":
		method = "jara/notify/test"
	default:
		if err := validChatPushURL(args); err != nil {
			u.print(err.Error() + "\n")
			return nil
		}
		method, params["url"] = "jara/notify/set", args
	}
	return u.requestWithAction(method, params, method)
}

func (u *chatUI) acceptChatNotifyResponse(m appMessage, p chatPending) {
	if m.Error != nil {
		u.printRole("system", "Notifications: "+chatText(m.Error.Message)+". Push notifications need a persistent Jara session.\n")
		return
	}
	var result struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(m.Result, &result)
	switch {
	case p.action == "jara/notify/test":
		u.printRole("system", "Sent a test notification.\n")
	case result.URL == "":
		u.printRole("system", "Push notifications are off. Use /notify URL with an ntfy.sh topic or a webhook to get them on your phone.\n")
	default:
		u.printRole("system", "Push notifications go to "+chatText(result.URL)+" when a long turn finishes or an approval or question waits, even while you are disconnected. /notify test sends one now; /notify off stops them.\n")
	}
}
