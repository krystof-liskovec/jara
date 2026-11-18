package protocol

import (
	"encoding/json"
	"strings"
)

// IsUsageLimit reports whether a failure is an account's usage limit, from
// Codex's error info or the message of either agent.
func IsUsageLimit(info json.RawMessage, message string) bool {
	lower := strings.ToLower(string(info) + " " + message)
	compact := strings.NewReplacer("_", "", "-", "", " ", "").Replace(lower)
	usageLimitMessage := strings.Contains(lower, "usage limit") && (strings.Contains(lower, "exceed") || strings.Contains(lower, "reach") || strings.Contains(lower, "hit"))
	return strings.Contains(compact, "usagelimitexceeded") || usageLimitMessage
}
