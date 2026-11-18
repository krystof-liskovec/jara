package jara

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/agentrouter"
)

func TestClaudeTokenReachesEveryHost(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	// A stand-in ssh records what each host receives; backup is offline.
	capture, bin := t.TempDir(), t.TempDir()
	t.Setenv("CAPTURE", capture)
	writeTestFile(t, bin, "ssh", `#!/bin/sh
case "$*" in *backup.example*) echo 'ssh: connect to host backup.example: No route to host' >&2; exit 255;; esac
for arg; do case "$arg" in *.example) host="$arg";; esac; done
printf '%s\n' "$*" > "$CAPTURE/$host.args"
cat > "$CAPTURE/$host.token"
`, time.Now())
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	c := Config{Remotes: map[string]Remote{"work": {Host: "work.example"}, "backup": {Host: "backup.example"}}, Order: []string{"work", "backup"}}

	report := shareClaudeToken(c, []byte("sk-ant-oat01-shared"))
	want := "This device: saved\nwork: saved\nbackup: not saved (exit status 255: ssh: connect to host backup.example: No route to host)\n"
	if !strings.HasPrefix(report, want) || !strings.Contains(report, "Run it again") {
		t.Fatalf("report:\n%s", report)
	}
	if local, _ := os.ReadFile(agentrouter.ClaudeTokenPath()); string(local) != "sk-ant-oat01-shared\n" {
		t.Fatalf("local %q", local)
	}
	sent, _ := os.ReadFile(filepath.Join(capture, "work.example.token"))
	args, _ := os.ReadFile(filepath.Join(capture, "work.example.args"))
	if string(sent) != "sk-ant-oat01-shared" || strings.Contains(string(args), "sk-ant") {
		t.Fatalf("work got %q with args %q", sent, args)
	}
	if got := shareClaudeToken(c, []byte("not-a-token")); !strings.HasPrefix(got, "Error: expected a Claude token") {
		t.Fatalf("bad token: %s", got)
	}
}
