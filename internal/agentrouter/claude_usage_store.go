package agentrouter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// Claude Code only reports usage while it runs, so the router keeps the
// newest report on disk: /usage and jara usage show it from any session on
// the host, with the time it was reported.

type claudeUsageRecord struct {
	ObservedAt int64             `json:"observedAt"`
	Limits     *claudeRateLimits `json:"limits"`
}

func claudeUsagePath() string {
	return filepath.Join(fsutil.DataRoot(), "claude-usage.json")
}

func saveClaudeUsage(info *claudeRateLimits) {
	_ = fsutil.WriteJSON(claudeUsagePath(), claudeUsageRecord{ObservedAt: time.Now().Unix(), Limits: info})
}

// ClaudeUsage answers in the account/rateLimits/read shape with the time of
// the report; rateLimits is null when Claude Code reported nothing here yet.
func ClaudeUsage() map[string]any {
	var record claudeUsageRecord
	if data, err := os.ReadFile(claudeUsagePath()); err != nil || json.Unmarshal(data, &record) != nil || record.Limits == nil {
		return map[string]any{"rateLimits": nil}
	}
	return map[string]any{"rateLimits": claudeLimitBucket(record.Limits), "observedAt": record.ObservedAt}
}
