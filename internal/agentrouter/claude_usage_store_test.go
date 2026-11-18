package agentrouter

import (
	"encoding/json"
	"testing"
)

func TestClaudeUsageKeepsTheNewestReport(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	if got := ClaudeUsage(); got["rateLimits"] != nil {
		t.Fatalf("before any report: %v", got)
	}
	r := &agentRouter{}
	r.observeClaudeRateLimits(json.RawMessage(`{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":0.42,"resetsAt":1900000000}}}`))
	got, _ := json.Marshal(ClaudeUsage()["rateLimits"])
	if string(got) != `{"primary":{"resetsAt":1900000000,"usedPercent":42,"windowDurationMins":300},"rateLimitReachedType":null,"secondary":null}` {
		t.Fatalf("%s", got)
	}
}
