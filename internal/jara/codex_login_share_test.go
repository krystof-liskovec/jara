package jara

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCodexHosts stands in for Codex on this device and on remotes reached
// over a fake ssh; each place keeps its sign-in in its own directory, and
// backup.example is offline.
func fakeCodexHosts(t *testing.T) (Config, string) {
	t.Helper()
	// Remembering accounts reads Codex's and Claude Code's sign-ins: keep
	// those and the pool in temporary directories.
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	state, bin := t.TempDir(), t.TempDir()
	writeTestFile(t, bin, "codex", `#!/bin/sh
mkdir -p "$STATE"
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"initialize"'*) echo "{\"id\":$id,\"result\":{}}" ;;
    *'"account/read"'*)
      if [ -f "$STATE/in" ]; then echo "{\"id\":$id,\"result\":{\"account\":{\"type\":\"chatgpt\",\"email\":\"me@example.com\",\"planType\":\"pro\"}}}"
      else echo "{\"id\":$id,\"result\":{\"account\":null,\"requiresOpenaiAuth\":true}}"; fi ;;
    *'"chatgptDeviceCode"'*)
      echo "{\"id\":$id,\"result\":{\"type\":\"chatgptDeviceCode\",\"loginId\":\"L\",\"verificationUrl\":\"https://auth.openai.com/codex/device\",\"userCode\":\"CODE-$(basename "$STATE")\"}}"
      touch "$STATE/in"
      echo '{"method":"account/login/completed","params":{"loginId":"L","success":true}}' ;;
    *'"apiKey"'*) printf '%s' "$line" > "$STATE/key"; echo "{\"id\":$id,\"result\":{\"type\":\"apiKey\"}}" ;;
  esac
done
`, time.Now())
	writeTestFile(t, bin, "ssh", `#!/bin/sh
case "$*" in *backup.example*) echo 'ssh: connect to host backup.example: No route to host' >&2; exit 255;; esac
for arg; do case "$arg" in *.example) host="$arg";; esac; done
case "$*" in *_rpc*) cat > /dev/null; echo "$host" >> "$STATES/remembered"; exit 0;; esac
STATE="$STATES/$host" exec "$FAKE_CODEX"
`, time.Now())
	for _, name := range []string{"codex", "ssh"} {
		if err := os.Chmod(filepath.Join(bin, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("JARA_CODEX_BIN", filepath.Join(bin, "codex"))
	t.Setenv("FAKE_CODEX", filepath.Join(bin, "codex"))
	t.Setenv("STATES", state)
	t.Setenv("STATE", filepath.Join(state, "local"))
	return Config{Remotes: map[string]Remote{"work": {Host: "work.example"}, "backup": {Host: "backup.example"}}, Order: []string{"work", "backup"}}, state
}

func TestCodexSignsInOnEveryHost(t *testing.T) {
	c, state := fakeCodexHosts(t)
	var steps []string
	report := codexDeviceLoginEverywhere(context.Background(), c, func(step string) { steps = append(steps, step) }, false)
	if len(steps) != 2 || !strings.Contains(steps[0], "Codex on This device (1 of 3)") || !strings.Contains(steps[0], "CODE-local") ||
		!strings.Contains(steps[1], "Codex on work (2 of 3)") || !strings.Contains(steps[1], "CODE-work.example") {
		t.Fatalf("steps %q", steps)
	}
	want := "This device: signed in as me@example.com · Pro\nwork: signed in as me@example.com · Pro\nbackup: not signed in ("
	if !strings.HasPrefix(report, want) || !strings.Contains(report, "Run it again") {
		t.Fatalf("report:\n%s", report)
	}
	// Signed-in hosts are skipped the next time.
	steps = nil
	if report = codexDeviceLoginEverywhere(context.Background(), c, func(step string) { steps = append(steps, step) }, false); len(steps) != 0 ||
		!strings.HasPrefix(report, "This device: already signed in as me@example.com · Pro\nwork: already signed in") {
		t.Fatalf("again: %q\n%s", steps, report)
	}
	// Cancelled before it starts: nothing is asked.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report = codexDeviceLoginEverywhere(ctx, c, func(string) { t.Fatal("asked after cancel") }, false); strings.Count(report, "(cancelled)") != 3 {
		t.Fatalf("cancelled:\n%s", report)
	}

	// Adding an account goes to every host, signed in or not, and keeps the
	// account in use there first.
	steps = nil
	report = codexDeviceLoginEverywhere(context.Background(), c, func(step string) { steps = append(steps, step) }, true)
	if len(steps) != 2 || !strings.HasPrefix(report, "This device: signed in as me@example.com · Pro\nwork: signed in as") {
		t.Fatalf("add: %q\n%s", steps, report)
	}
	if remembered, _ := os.ReadFile(filepath.Join(state, "remembered")); string(remembered) != "work.example\nwork.example\n" {
		t.Fatalf("remembered %q", remembered)
	}

	report = shareCodexAPIKey(c, " sk-proj-0123456789abcdefghij ")
	if !strings.HasPrefix(report, "This device: saved\nwork: saved\nbackup: not saved (") {
		t.Fatalf("key report:\n%s", report)
	}
	for _, place := range []string{"local", "work.example"} {
		if key, _ := os.ReadFile(filepath.Join(state, place, "key")); !strings.Contains(string(key), `"apiKey":"sk-proj-0123456789abcdefghij"`) {
			t.Fatalf("%s got %q", place, key)
		}
	}
	if got := shareCodexAPIKey(c, "short"); !strings.HasPrefix(got, "Error: expected an OpenAI API key") {
		t.Fatalf("bad key: %s", got)
	}
}

func TestChatShowsEachHostsCodexCode(t *testing.T) {
	c, _ := fakeCodexHosts(t)
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}
	term, _ := newTestChatTerminal(t)
	term.u.c = offlineChatConnection()
	if _, err := term.u.line("/login codex device everywhere"); err != nil || term.u.login.stopEverywhere == nil {
		t.Fatalf("start: %v", err)
	}
	for deadline := time.After(10 * time.Second); term.u.backgroundRuns != nil; {
		select {
		case note := <-term.u.backgroundRuns:
			term.u.acceptChatBackground(note)
		case <-deadline:
			t.Fatal("never finished")
		}
	}
	screen := plainChatScreen(term.frame(160, 60, time.Now()))
	for _, want := range []string{"Codex on This device (1 of 3)", "CODE-local", "Codex on work (2 of 3)", "Codex sign-in", "work: signed in as me@example.com", "backup: not signed in"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("missing %q:\n%s", want, screen)
		}
	}
	if term.u.login.stopEverywhere != nil {
		t.Fatal("the stop stayed armed")
	}
}
