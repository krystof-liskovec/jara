package jara

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPlanUpdatesOffersWhatIsBehind(t *testing.T) {
	now := time.Now()
	saved := buildStampData
	t.Cleanup(func() { buildStampData = saved })
	buildStampData = []byte(`{"time":2000,"source":"new"}`)
	c := Config{
		Remotes:         map[string]Remote{"work": {Host: "w"}, "old": {Host: "o"}, "same": {Host: "s"}, "bare": {Host: "b"}, "down": {Host: "d"}},
		HostDeployments: map[string]HostDeployment{"work": {}, "old": {}, "same": {}, "down": {}},
	}
	check := updateCheck{
		Latest: map[string]string{"codex": "0.171.0", "claude": "2.1.300"},
		Remotes: map[string]remoteVersions{
			// An earlier build of this version, Codex behind and signed in, Claude Code not signed in.
			"work": {machineVersions: machineVersions{Jara: Version, Build: buildStamp{Time: 1000, Source: "old"}, Codex: "0.169.0", CodexAuth: true, Claude: "2.1.1"}},
			// A Jara from before the versions op.
			"old": {machineVersions: machineVersions{Jara: Version}, Partial: true},
			// This very build, everything current.
			"same": {machineVersions: machineVersions{Jara: Version, Build: buildStamp{Time: 2000, Source: "new"}, Codex: "0.172.0", CodexAuth: true}},
			// Older, but without a Docker host connection to upgrade it.
			"bare": {machineVersions: machineVersions{Jara: "0.0.1", Codex: "0.1.0", CodexAuth: true}},
			"down": {Error: "unreachable"},
		},
		Snoozed: map[string]int64{},
	}
	// This device has a newer Codex than the release check saw, and Claude
	// Code behind but not signed in.
	here := machineVersions{Codex: "0.172.0", CodexAuth: true, Claude: "2.1.200"}
	plan := planUpdates(c, check, here, now)
	if strings.Join(sortedKeys(plan.jara), ",") != "bare,old,work" || plan.jara["bare"] || !plan.jara["work"] {
		t.Fatalf("jara %v", plan.jara)
	}
	if len(plan.agents) != 1 || plan.agents[0].agent != "codex" || plan.agents[0].target != "0.172.0" || plan.agents[0].local != "" ||
		strings.Join(sortedKeys(plan.agents[0].remotes), ",") != "work" {
		t.Fatalf("agents %+v", plan.agents)
	}
	// This device behind and signed in: offered here too.
	here.CodexAuth, here.Codex = true, "0.170.0"
	check.Remotes["same"] = remoteVersions{machineVersions: machineVersions{Jara: Version, Build: buildStamp{Time: 2000, Source: "new"}}}
	plan = planUpdates(c, check, here, now)
	if plan.agents[0].target != "0.171.0" || plan.agents[0].local != "0.170.0" {
		t.Fatalf("local %+v", plan.agents[0])
	}
	// Declined offers stay quiet until the snooze ends.
	for _, key := range plan.snoozeIn {
		check.Snoozed[key] = now.Add(time.Hour).Unix()
	}
	if plan = planUpdates(c, check, here, now); !plan.empty() {
		t.Fatalf("snoozed %+v", plan)
	}
	if plan = planUpdates(c, check, here, now.Add(2*time.Hour)); plan.empty() {
		t.Fatal("the snooze never ended")
	}
}

func TestAskUpdatesRecordsChoices(t *testing.T) {
	plan := updatePlan{
		jara: map[string]bool{"work": true, "bare": false},
		agents: []agentUpdate{
			{agent: "codex", name: "Codex", target: "0.171.0", local: "0.170.0", remotes: map[string]string{"work": "0.169.0"}},
			{agent: "claude", name: "Claude Code", target: "2.1.300", local: "2.1.200"},
		},
		snoozeIn: []string{"jara/work/v/s", "jara/bare/v/s", "codex/local/0.171.0", "codex/work/0.171.0", "claude/local/2.1.300"},
	}
	var out bytes.Buffer
	// Jara: yes; Codex: this device only; Claude Code: no.
	choices, err := askUpdates(plan, bufio.NewReader(strings.NewReader("\n2\nn\n")), &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(choices.local, ",") != "codex" || !choices.remotes["work"]["jara"] || choices.remotes["work"]["codex"] {
		t.Fatalf("choices %+v", choices)
	}
	if strings.Join(choices.snooze, ",") != "codex/work/0.171.0,claude/local/2.1.300" {
		t.Fatalf("snoozed %q", choices.snooze)
	}
	for _, want := range []string{"bare runs an older Jara than this client. Jara has no Docker host connection", "work runs an older Jara than this client. Update it now?",
		"Codex 0.171.0 is out; this device has 0.170.0, work 0.169.0.", "1) Update this device and the remotes", "Claude Code 2.1.300 is out; this device has 2.1.200. Update it now?"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in\n%s", want, out.String())
		}
	}
}

func TestBuildStampsAndClaudePins(t *testing.T) {
	older, newer := buildStamp{Time: 1, Source: "a"}, buildStamp{Time: 2, Source: "b"}
	if !older.olderThan(newer) || newer.olderThan(older) || older.olderThan(buildStamp{Time: 2, Source: "a"}) || (buildStamp{}).olderThan(newer) {
		t.Fatal("olderThan")
	}
	var log bytes.Buffer
	settings := map[string]string{"CLAUDE_CODE_VERSION": "2.1.1"}
	if got := newerClaudePin(settings, func() (string, error) { return "2.1.5", nil }, &log); got != "2.1.5" || settings["CLAUDE_CODE_VERSION"] != "2.1.5" {
		t.Fatalf("pin %q", got)
	}
	if got := newerClaudePin(map[string]string{"CLAUDE_CODE_VERSION": ""}, func() (string, error) { return "2.1.5", nil }, &log); got != "" {
		t.Fatal("raised a disabled Claude Code")
	}
	if got := string(setEnvSetting([]byte("A=1\nCLAUDE_CODE_VERSION=2.1.1\nB=2\n"), "CLAUDE_CODE_VERSION", "2.1.5")); got != "A=1\nCLAUDE_CODE_VERSION=2.1.5\nB=2\n" {
		t.Fatalf("env %q", got)
	}
}
