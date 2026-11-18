package jara

import (
	"encoding/json"
	"fmt"

	"krystof.liskovec/jara/internal/protocol"
)

// /compact idle shows or changes idle compaction, which the session daemon
// runs before the prompt cache of an idle, large conversation expires.

func (u *chatUI) runChatIdleCompactCommand(args []string) error {
	if len(args) > 1 {
		u.print("Use /compact idle [on|off|DURATION|status].\n")
		return nil
	}
	method, params := "jara/idleCompaction/get", map[string]any{}
	if len(args) == 1 {
		switch args[0] {
		case "status":
		case "on":
			method, params["enabled"] = "jara/idleCompaction/set", true
		case "off":
			method, params["enabled"] = "jara/idleCompaction/set", false
		default:
			if _, err := parseChatIdleCompactAfter(args[0]); err != nil {
				u.print("Idle compaction: " + err.Error() + ".\n")
				return nil
			}
			method, params["enabled"], params["after"] = "jara/idleCompaction/set", true, args[0]
		}
	}
	return u.requestWithAction(method, params, method)
}

func (u *chatUI) acceptChatIdleCompactResponse(m appMessage) {
	if m.Error != nil {
		u.printRole("system", "Idle compaction: "+chatText(m.Error.Message)+". Idle compaction needs a persistent Jara session.\n")
		return
	}
	var result struct {
		Enabled bool   `json:"enabled"`
		Agent   string `json:"agent"`
		After   string `json:"after"`
	}
	_ = json.Unmarshal(m.Result, &result)
	if !result.Enabled {
		u.printRole("system", "Idle compaction is off. /compact idle on turns it back on.\n")
		return
	}
	u.printRole("system", fmt.Sprintf("Idle compaction is on: a %s conversation using at least %d%% of its context is compacted after %s idle, before the prompt cache expires. /compact idle DURATION changes the delay for %s; /compact idle off turns it off.\n",
		protocol.AgentDisplayName(result.Agent), chatIdleCompactPercent, chatText(result.After), protocol.AgentDisplayName(result.Agent)))
}
