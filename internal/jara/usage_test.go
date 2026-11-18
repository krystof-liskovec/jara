package jara

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatUsageShowsEveryWindow(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.Local)
	at := func(d time.Duration) int64 { return now.Add(d).Unix() }
	codex := fmt.Sprintf(`{"rateLimits":{"limitId":"codex","planType":"pro","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":%d},
		"secondary":{"usedPercent":8,"windowDurationMins":10080,"resetsAt":%d},"rateLimitReachedType":"primary",
		"credits":{"hasCredits":true,"unlimited":false,"balance":"61428.4375680000"}},
		"rateLimitsByLimitId":{"codex":{"limitId":"codex"},"spark":{"limitName":"Spark","primary":{"usedPercent":40,"windowDurationMins":300,"resetsAt":%d}}}}`,
		at(2*time.Hour), at(6*24*time.Hour), at(-time.Hour))
	claude := fmt.Sprintf(`{"rateLimits":{"primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":%d},"secondary":null},"observedAt":%d}`,
		at(3*time.Hour), at(-12*time.Minute))
	got := formatUsage(usageReport{Codex: json.RawMessage(codex), Claude: json.RawMessage(claude)}, now)
	for _, want := range []string{
		"Codex · Pro\n",
		"  5-hour    ████████████████████ 100% · resets 14:00 · limit reached\n",
		"  Weekly    ██░░░░░░░░░░░░░░░░░░   8% · resets Tue 12:00\n",
		"  Spark 5-hour ░░░░░░░░░░░░░░░░░░░░   0% · reset since 11:00\n",
		"  Credits   61428.44\n",
		"Claude Code · reported 12 min ago\n",
		"  5-hour    ████████░░░░░░░░░░░░  42% · resets 15:00\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Count(got, "Weekly") != 1 {
		t.Fatalf("the main bucket repeated:\n%s", got)
	}
	if got = formatUsage(usageReport{CodexError: "unavailable: not logged in", Claude: json.RawMessage(`{"rateLimits":null}`)}, now); got !=
		"Codex · unavailable: not logged in\nClaude Code · nothing reported yet; Claude Code reports usage during a turn\n" {
		t.Fatalf("%q", got)
	}
}

func TestChatUsageListsEveryPlace(t *testing.T) {
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("JARA_CODEX_BIN", "/nonexistent/codex")
	t.Setenv("JARA_CLAUDE_BIN", "/nonexistent/claude")
	t.Setenv("PATH", t.TempDir()) // no agents and no ssh: the remote is unreachable
	if err := saveConfig(Config{Remotes: map[string]Remote{"work": {Host: "work.example"}}}); err != nil {
		t.Fatal(err)
	}
	term, _ := newTestChatTerminal(t)
	if _, err := term.u.line("/usage"); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-term.u.backgroundRuns:
		term.u.acceptChatBackground(text)
	case <-time.After(5 * time.Second):
		t.Fatal("/usage never finished")
	}
	screen := plainChatScreen(term.frame(140, 40, time.Now()))
	if !strings.Contains(screen, "Usage limits") || !strings.Contains(screen, "work · unreachable") || strings.Contains(screen, "This device") {
		t.Fatalf("%s", screen)
	}
	if _, err := term.u.line("/usage nowhere"); err != nil || term.u.backgroundRuns != nil {
		t.Fatalf("an unknown remote ran: %v", err)
	}
}
