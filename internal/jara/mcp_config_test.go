package jara

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

func isolateMCP(t *testing.T) {
	t.Helper()
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("JARA_CODEX_ARGS", "[]")
	t.Setenv("JARA_CLAUDE_ARGS", "[]")
}

func TestMCPServerListIsValidated(t *testing.T) {
	for name, servers := range map[string]mcpServers{
		"bad name":        {"has space": {Command: "x"}},
		"reserved":        {"jara": {Command: "x"}},
		"neither":         {"a": {}},
		"both":            {"a": {Command: "x", URL: "https://x"}},
		"url with env":    {"a": {URL: "https://x", Env: map[string]string{"K": "V"}}},
		"command headers": {"a": {Command: "x", Headers: map[string]string{"K": "V"}}},
		"scheme":          {"a": {URL: "ftp://x"}},
	} {
		if validateMCPServers(servers) == nil {
			t.Errorf("%s: accepted %+v", name, servers)
		}
	}
	if err := validateMCPServers(mcpServers{"docs": {URL: "https://x/mcp", Headers: map[string]string{"t": "1"}}, "repl": {Command: "node", Args: []string{"repl.js"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsStartWithJaraMCPServers(t *testing.T) {
	isolateMCP(t)
	servers := mcpServers{
		"docs": {URL: "https://docs.example/mcp", Headers: map[string]string{"x-token": "secret"}},
		"repl": {Command: "node", Args: []string{"repl.js", `a "quoted" arg`}, Env: map[string]string{"MODE": "dev"}},
	}
	if err := saveMCPServers(servers); err != nil {
		t.Fatal(err)
	}
	// Codex gets -c overrides first, so saved arguments can still change them.
	codex, err := configuredAgentArgs(protocol.AgentCodex, []string{"--model", "gpt-6"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-c", `mcp_servers.docs.url="https://docs.example/mcp"`,
		"-c", `mcp_servers.docs.http_headers={"x-token"="secret"}`,
		"-c", `mcp_servers.repl.command="node"`,
		"-c", `mcp_servers.repl.args=["repl.js","a \"quoted\" arg"]`,
		"-c", `mcp_servers.repl.env={"MODE"="dev"}`,
		"--model", "gpt-6",
	}
	if strings.Join(codex, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Codex arguments:\n%s", strings.Join(codex, "\n"))
	}
	// Claude Code reads a generated file.
	claude, err := configuredAgentArgs(protocol.AgentClaude, nil)
	if err != nil || len(claude) != 1 || claude[0] != "--mcp-config="+mcpClaudeConfigPath() {
		t.Fatalf("Claude Code arguments %q %v", claude, err)
	}
	var file struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	data, _ := os.ReadFile(mcpClaudeConfigPath())
	if json.Unmarshal(data, &file) != nil || file.Servers["docs"]["type"] != "http" || file.Servers["repl"]["command"] != "node" {
		t.Fatalf("Claude Code config:\n%s", data)
	}
	if info, _ := os.Stat(mcpClaudeConfigPath()); info.Mode().Perm() != 0600 {
		t.Fatalf("the config holds tokens; mode %v", info.Mode().Perm())
	}
	// Showing the saved arguments leaves the servers out.
	if saved, _ := savedAgentArgs(protocol.AgentCodex, nil); len(saved) != 0 {
		t.Fatalf("saved arguments %q", saved)
	}
	// Delegated tasks get them too.
	task := delegatedTask{Agent: protocol.AgentCodex, Session: "s"}
	if command := delegatedCommand(task, 1); command[1] != "-c" || !strings.HasPrefix(command[2], "mcp_servers.") {
		t.Fatalf("delegated Codex %q", command)
	}
	task.Agent = protocol.AgentClaude
	if command := strings.Join(delegatedCommand(task, 1), " "); !strings.Contains(command, "--mcp-config=") {
		t.Fatalf("delegated Claude Code %q", command)
	}
	// Without servers nothing is added.
	if err = saveMCPServers(mcpServers{}); err != nil {
		t.Fatal(err)
	}
	if args, _ := configuredAgentArgs(protocol.AgentClaude, nil); len(args) != 0 {
		t.Fatalf("empty list added %q", args)
	}
}

func TestHostServesTheMCPServerList(t *testing.T) {
	isolateMCP(t)
	if err := mcpRPC("set", strings.NewReader(`{"docs":{"url":"https://docs.example/mcp"}}`)); err != nil {
		t.Fatal(err)
	}
	if servers, err := loadMCPServers(); err != nil || servers["docs"].URL != "https://docs.example/mcp" {
		t.Fatalf("saved %+v %v", servers, err)
	}
	if err := mcpRPC("set", strings.NewReader(`{"jara":{"command":"x"}}`)); err == nil {
		t.Fatal("an invalid list was saved")
	}
}

func TestMCPImportReadsBothAgentsConfigs(t *testing.T) {
	isolateMCP(t)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	writeTestFile(t, claudeDir, ".claude.json", `{"mcpServers":{
		"docs":{"type":"http","url":"https://docs.example/mcp","headers":{"x-token":"from-claude"}},
		"repl":{"command":"node","args":["repl.js"]},
		"old":{"type":"sse","url":"https://old.example/sse"},
		"jara":{"type":"stdio","command":"/usr/local/bin/jara","args":["mcp"]}}}`, time.Now())
	// A stand-in codex answers `codex mcp list --json`.
	bin := t.TempDir()
	writeTestFile(t, bin, "codex", "#!/bin/sh\ncat <<'EOF'\n"+`[
		{"name":"docs","enabled":true,"transport":{"type":"streamable_http","url":"https://docs.example/mcp","http_headers":{"x-token":"from-codex"}}},
		{"name":"logs","enabled":true,"transport":{"type":"streamable_http","url":"https://logs.example/mcp"}},
		{"name":"off","enabled":false,"transport":{"type":"stdio","command":"x"}}]`+"\nEOF\n", time.Now())
	if err := os.Chmod(filepath.Join(bin, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	servers, notes := importLocalMCPServers("")
	if got := strings.Join(sortedMCPNames(servers), ","); got != "docs,logs,repl" {
		t.Fatalf("imported %s; notes %q", got, notes)
	}
	if servers["docs"].Headers["x-token"] != "from-claude" || servers["repl"].Command != "node" {
		t.Fatalf("servers %+v", servers)
	}
	if joined := strings.Join(notes, "\n"); !strings.Contains(joined, "old: sse") || !strings.Contains(joined, "off: disabled") {
		t.Fatalf("notes %q", notes)
	}
	if only, _ := importLocalMCPServers("codex"); only["docs"].Headers["x-token"] != "from-codex" || len(only) != 2 {
		t.Fatalf("Codex only %+v", only)
	}
}

func TestSharedMCPListReachesEveryPlaceItRuns(t *testing.T) {
	isolateMCP(t)
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	// A stand-in ssh records what each host receives; FAIL_HOST is offline.
	capture, bin := t.TempDir(), t.TempDir()
	t.Setenv("CAPTURE", capture)
	writeTestFile(t, bin, "ssh", `#!/bin/sh
case "$*" in *"$FAIL_HOST"*) echo unreachable >&2; exit 255;; esac
for arg; do case "$arg" in *.example) host="$arg";; esac; done
cat > "$CAPTURE/$host.$(ls "$CAPTURE" | wc -l)"
`, time.Now())
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("FAIL_HOST", "backup.example")
	c := Config{Remotes: map[string]Remote{"work": {Host: "work.example"}, "backup": {Host: "backup.example"}}}
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		t.Helper()
		config, err := readConfig()
		if err != nil {
			t.Fatal(err)
		}
		return mcpConfigCommand(&config, args, io.Discard)
	}
	received := func(host string) []string {
		t.Helper()
		entries, _ := os.ReadDir(capture)
		var payloads []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), host+".") {
				data, _ := os.ReadFile(filepath.Join(capture, entry.Name()))
				payloads = append(payloads, string(data))
			}
		}
		return payloads
	}
	localNames := func() string {
		servers, _ := loadMCPServers()
		return strings.Join(sortedMCPNames(servers), ",")
	}

	// A new server runs everywhere: here and on every reachable remote.
	if err := run("add", "docs", "--url", "https://docs.example/mcp", "--header", "x-token=1"); err != nil {
		t.Fatal(err)
	}
	if localNames() != "docs" || len(received("work.example")) != 1 || !strings.Contains(received("work.example")[0], `"docs"`) {
		t.Fatalf("after add: local %q, work %q", localNames(), received("work.example"))
	}
	// One limited to this device leaves the remotes' share alone.
	if err := run("add", "repl", "--on", "local", "--", "node", "repl.js"); err != nil {
		t.Fatal(err)
	}
	if localNames() != "docs,repl" || len(received("work.example")) != 1 {
		t.Fatalf("after a local-only add: local %q, work sent %d times", localNames(), len(received("work.example")))
	}
	// Moving docs to work only takes it off this device.
	if err := run("where", "docs", "work"); err != nil {
		t.Fatal(err)
	}
	if localNames() != "repl" || len(received("work.example")) != 1 {
		t.Fatalf("after where: local %q, work sent %d times", localNames(), len(received("work.example")))
	}
	if err := run("where", "docs", "elsewhere"); err == nil {
		t.Fatal("an unknown place was accepted")
	}
	// The offline remote catches up when a session opens there.
	t.Setenv("FAIL_HOST", "nowhere")
	syncMCPToRemote(Remote{Host: "backup.example"})
	if got := received("backup.example"); len(got) != 1 || strings.TrimSpace(got[0]) != "{}" {
		t.Fatalf("backup received %q", got)
	}
	syncMCPToRemote(Remote{Host: "backup.example"})
	if len(received("backup.example")) != 1 {
		t.Fatal("an unchanged share was sent again")
	}
	if err := run("add", "bad", "--header", "nokey"); err == nil {
		t.Fatal("a header without = was accepted")
	}
	if err := run("remove", "missing"); err == nil {
		t.Fatal("removing an unknown server succeeded")
	}
}
