package jara

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAgentMetadataAndProcessValidation(t *testing.T) {
	if _, ok := parseAgentMetadata("work", "running", "42"); !ok {
		t.Fatal("valid running metadata was rejected")
	}
	for _, test := range []struct{ state, pid string }{{"unknown", "42"}, {"running", "0"}, {"running", "bad"}} {
		if _, ok := parseAgentMetadata("work", test.state, test.pid); ok {
			t.Fatalf("accepted invalid metadata: state=%q pid=%q", test.state, test.pid)
		}
	}
	if !agentStateProtectsFromCleanup("running") || !agentStateProtectsFromCleanup("waiting") || !agentStateProtectsFromCleanup("starting") {
		t.Fatal("active agent states must protect sessions")
	}
	if agentStateProtectsFromCleanup("idle") || agentStateProtectsFromCleanup("stale") {
		t.Fatal("idle or unknown state must not protect sessions")
	}

	processes := parseAgentProcessTable("100 1 /bin/bash\n101 100 /usr/local/bin/jara _chat token\n102 100 /usr/local/bin/jara menu\n")
	if !isJaraChatProcess(processes[101].args) || !descendsFromPane(101, map[int]bool{100: true}, processes) {
		t.Fatal("live Jara chat descendant was not recognized")
	}
	if isJaraChatProcess(processes[102].args) || descendsFromPane(101, map[int]bool{999: true}, processes) {
		t.Fatal("unrelated Jara process or foreign pane was accepted")
	}
}

func TestSessionItemShowsLiveAgentState(t *testing.T) {
	item := sessionItem(Workspace{ID: "w-test"}, Session{AgentState: "waiting"}, 1)
	if !strings.Contains(item.Detail, "Agent waiting") {
		t.Fatalf("agent state missing from terminal menu detail: %q", item.Detail)
	}
	item = sessionItem(Workspace{ID: "w-test"}, Session{AgentState: "stale"}, 1)
	if strings.Contains(item.Detail, "Agent") {
		t.Fatalf("unknown agent state shown in terminal menu detail: %q", item.Detail)
	}
}

func TestAgentProcessFixture(t *testing.T) {
	if os.Getenv("JARA_AGENT_FIXTURE") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestExpireDetachedSessionsProtectsLiveAgentsAndExpiresStaleMetadata(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	if err := tmux("new-session", "-d", "-s", "server-ready", "/bin/sleep 300").Run(); err != nil {
		t.Skipf("cannot start isolated tmux server: %v", err)
	}
	t.Cleanup(func() { _ = tmux("kill-server").Run() })

	dir := t.TempDir()
	busy := "live-agent"
	fixture, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureCommand := "export JARA_AGENT_FIXTURE=1; exec -a 'jara _chat' " + shellQuote(fixture) + " -test.run='^TestAgentProcessFixture$'"
	createManagedLifecycleSession(t, busy, dir, "/bin/bash -c "+shellQuote(fixtureCommand))
	panePIDOutput, err := tmux("list-panes", "-t", busy, "-F", "#{pane_pid}").Output()
	if err != nil {
		t.Fatal(err)
	}
	panePID := strings.TrimSpace(string(panePIDOutput))
	if err := runTmux("set-option", "-t", busy, "@jara_agent_pid", panePID); err != nil {
		t.Fatal(err)
	}
	if err := runTmux("set-option", "-t", busy, "@jara_agent_state", "running"); err != nil {
		t.Fatal(err)
	}
	waitForLiveAgent(t, busy, panePID)

	stale := "stale-agent"
	createManagedLifecycleSession(t, stale, dir, "/bin/sleep 300")
	if err := runTmux("set-option", "-t", stale, "@jara_agent_state", "running"); err != nil {
		t.Fatal(err)
	}
	if err := runTmux("set-option", "-t", stale, "@jara_agent_pid", "2147483647"); err != nil {
		t.Fatal(err)
	}

	legacy := "legacy-session"
	createManagedLifecycleSession(t, legacy, dir, "/bin/sleep 300")

	rows, err := sessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == busy && row.AgentState != "running" {
			t.Fatalf("menu state = %q; want running", row.AgentState)
		}
	}

	if err := expireDetachedSessions(time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	for name, wantAlive := range map[string]bool{busy: true, stale: false, legacy: false} {
		alive := tmux("has-session", "-t", name).Run() == nil
		if alive != wantAlive {
			t.Fatalf("session %s alive=%v, want %v", name, alive, wantAlive)
		}
	}
}

func TestExpireDetachedSessionsStopsWhenAgentProcessObservationFails(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	if err := tmux("new-session", "-d", "-s", "server-ready", "/bin/sleep 300").Run(); err != nil {
		t.Skipf("cannot start isolated tmux server: %v", err)
	}
	t.Cleanup(func() { _ = tmux("kill-server").Run() })

	dir := t.TempDir()
	session := "active-agent"
	fixture, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureCommand := "export JARA_AGENT_FIXTURE=1; exec -a 'jara _chat' " + shellQuote(fixture) + " -test.run='^TestAgentProcessFixture$'"
	createManagedLifecycleSession(t, session, dir, "/bin/bash -c "+shellQuote(fixtureCommand))
	panePIDOutput, err := tmux("list-panes", "-t", session, "-F", "#{pane_pid}").Output()
	if err != nil {
		t.Fatal(err)
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(string(panePIDOutput)))
	if err != nil {
		t.Fatalf("invalid pane PID: %v", err)
	}
	if err := runTmux("set-option", "-t", session, "@jara_agent_pid", strconv.Itoa(panePID)); err != nil {
		t.Fatal(err)
	}
	if err := runTmux("set-option", "-t", session, "@jara_agent_state", "running"); err != nil {
		t.Fatal(err)
	}
	waitForLiveAgent(t, session, strconv.Itoa(panePID))

	bin := t.TempDir()
	psWrapper := filepath.Join(bin, "ps")
	if err := os.WriteFile(psWrapper, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+oldPath)
	if err := expireDetachedSessions(time.Now(), time.Hour); err == nil {
		t.Fatal("cleanup succeeded despite process observation failure")
	}
	if err := tmux("has-session", "-t", session).Run(); err != nil {
		t.Fatalf("active agent session was expired after observation failure: %v", err)
	}
}

func createManagedLifecycleSession(t *testing.T, name, dir, command string) {
	t.Helper()
	if err := tmux("new-session", "-d", "-s", name, "-c", dir, command).Run(); err != nil {
		t.Fatalf("create tmux session %s: %v", name, err)
	}
	old := strconv.FormatInt(time.Now().Add(-2*time.Hour).UnixNano(), 10)
	for option, value := range map[string]string{"@jara_workspace": "w-test", "@jara_last_attached": old, "@jara_idle_hook": "1"} {
		if err := runTmux("set-option", "-t", name, option, value); err != nil {
			t.Fatalf("set %s for %s: %v", option, name, err)
		}
	}
}

func waitForLiveAgent(t *testing.T, session, rawPID string) {
	t.Helper()
	pid, err := strconv.Atoi(rawPID)
	if err != nil {
		t.Fatalf("invalid pane PID %q: %v", rawPID, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		states, err := liveAgentStates([]agentMetadata{{session: session, state: "running", pid: pid}})
		if err == nil && states[session] == "running" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("agent process in %s did not become live", session)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
