package jara

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

func mcpExchange(t *testing.T, requests ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := serveMCP(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var responses []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(out.String()))
	for scanner.Scan() {
		var response map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatalf("invalid response %q", scanner.Text())
		}
		responses = append(responses, response)
	}
	return responses
}

func TestMCPHandshakeAndTools(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	responses := mcpExchange(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_tasks","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"delegate_task","arguments":{"agent":"gemini","prompt":"x"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	)
	if len(responses) != 5 {
		t.Fatalf("expected 5 responses (notification unanswered), got %d", len(responses))
	}
	byID := map[float64]map[string]any{}
	for _, r := range responses {
		byID[r["id"].(float64)] = r
	}
	if byID[1]["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocol not negotiated: %v", byID[1])
	}
	var names []string
	for _, tool := range byID[2]["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	for _, want := range []string{"delegate_task", "task_status", "continue_task", "list_tasks", "merge_task", "cancel_task"} {
		if !slices.Contains(names, want) {
			t.Fatalf("missing tool %s in %v", want, names)
		}
	}
	if !strings.Contains(toolText(byID[3]), "No delegated tasks") {
		t.Fatalf("list_tasks: %v", byID[3])
	}
	if byID[4]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("bad agent should be a tool error: %v", byID[4])
	}
	if byID[5]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", byID[5])
	}
}

func toolText(response map[string]any) string {
	content := response["result"].(map[string]any)["content"].([]any)
	return content[0].(map[string]any)["text"].(string)
}

func TestDelegatedCommands(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	claude := delegatedTask{ID: "t-0123456789ab", Agent: protocol.AgentClaude, Permission: permissionEdit, Session: "uuid", Model: "opus"}
	got := delegatedCommand(claude, 1)
	if got[0] != protocol.ClaudeBinary || !slices.Contains(got, "--print") || !containsPair(got, "--session-id", "uuid") || !containsPair(got, "--permission-mode", "acceptEdits") || !containsPair(got, "--permission-prompts", "none") {
		t.Fatalf("claude first run: %q", got)
	}
	if !containsPair(delegatedCommand(claude, 2), "--resume", "uuid") {
		t.Fatal("claude follow-up must resume the session")
	}
	claude.Permission = permissionReadOnly
	if !containsPair(delegatedCommand(claude, 1), "--permission-mode", "plan") {
		t.Fatal("read-only claude must use plan mode")
	}
	codex := delegatedTask{ID: "t-0123456789ab", Agent: protocol.AgentCodex, Permission: permissionEdit, Session: "thread"}
	got = delegatedCommand(codex, 1)
	if !slices.Equal(got[:2], []string{protocol.CodexBinary, "exec"}) || got[len(got)-1] != "-" || !slices.Contains(got, `sandbox_mode="workspace-write"`) {
		t.Fatalf("codex first run: %q", got)
	}
	got = delegatedCommand(codex, 2)
	if got[2] != "resume" || got[len(got)-2] != "thread" || got[len(got)-1] != "-" {
		t.Fatalf("codex follow-up: %q", got)
	}
	codex.Permission = permissionFull
	if !slices.Contains(delegatedCommand(codex, 1), "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatal("full codex permission")
	}
}

func containsPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestRenderCodexEvents(t *testing.T) {
	events := strings.Join([]string{
		`{"type":"thread.started","thread_id":"abc-123"}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./..."}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"All tests pass."}}`,
		`not json`,
	}, "\n")
	var terminal, log strings.Builder
	if thread := renderCodexEvents(strings.NewReader(events), &terminal, &log); thread != "abc-123" {
		t.Fatalf("thread %q", thread)
	}
	for _, want := range []string{"$ go test ./...", "All tests pass.", "not json"} {
		if !strings.Contains(terminal.String(), want) || !strings.Contains(log.String(), want) {
			t.Fatalf("missing %q in %q", want, terminal.String())
		}
	}
}

func gitFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}} {
		if _, err := gitIn(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitIn(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitIn(repo, "commit", "-qm", "init"); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestTaskWorktreeMerge(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	repo := gitFixture(t)
	task := delegatedTask{ID: "t-00000000000a", Agent: protocol.AgentClaude, Title: "add b", OriginCWD: repo, Status: taskSucceeded}
	if err := prepareTaskWorktree(&task); err != nil {
		t.Fatal(err)
	}
	if task.CWD != task.Worktree || task.Branch != "jara/t-00000000000a" {
		t.Fatalf("worktree task: %+v", task)
	}
	if err := os.WriteFile(filepath.Join(task.Worktree, "b.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err == nil {
		t.Fatal("worktree changes leaked into the caller checkout")
	}
	report, err := mergeTask(task, true)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "b.txt")); string(data) != "two\n" {
		t.Fatalf("merge did not bring changes: %s", report)
	}
	if _, err := os.Stat(task.Worktree); !os.IsNotExist(err) {
		t.Fatal("cleanup should remove the worktree")
	}
	task.Status = taskRunning
	if _, err = mergeTask(task, false); err == nil {
		t.Fatal("running tasks cannot be merged")
	}
}

func TestDelegationRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JARA_DATA", filepath.Join(home, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	existing := "model = \"gpt-6.1-sol\"\n"
	_ = os.MkdirAll(filepath.Join(home, "codex"), 0700)
	_ = os.WriteFile(codexConfigPath(), []byte(existing), 0600)
	for i := 0; i < 2; i++ {
		if err := registerCodexMCP(); err != nil {
			t.Fatal(err)
		}
		if err := registerClaudeMCP(); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(codexConfigPath())
	if !strings.HasPrefix(string(data), existing) || strings.Count(string(data), "[mcp_servers.jara]") != 1 || !strings.Contains(string(data), "tool_timeout_sec = 3600") {
		t.Fatalf("codex config: %s", data)
	}
	var settings map[string]any
	raw, _ := os.ReadFile(claudeConfigPath())
	_ = json.Unmarshal(raw, &settings)
	if settings["mcpServers"].(map[string]any)["jara"].(map[string]any)["command"] != jaraBinary {
		t.Fatalf("claude config: %s", raw)
	}
	if err := unregisterCodexMCP(); err != nil {
		t.Fatal(err)
	}
	if err := unregisterClaudeMCP(); err != nil {
		t.Fatal(err)
	}
	if data, _ = os.ReadFile(codexConfigPath()); string(data) != existing {
		t.Fatalf("codex config not restored: %q", data)
	}
	raw, _ = os.ReadFile(claudeConfigPath())
	if strings.Contains(string(raw), "\"jara\"") {
		t.Fatalf("claude entry not removed: %s", raw)
	}
}

func TestTaskStateAndLimits(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	if _, err := taskDir("../escape"); err == nil {
		t.Fatal("task IDs must be validated")
	}
	task := delegatedTask{ID: "t-00000000000b", Agent: protocol.AgentCodex, Status: taskSucceeded, Runs: 1, Title: "x"}
	_ = os.MkdirAll(filepath.Join(tasksRoot(), task.ID), 0700)
	if err := saveTask(task); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(taskFile(task, "log", 1), []byte(strings.Repeat("x", 100)+"END"), 0600)
	if got := taskResult(task, 10); !strings.HasSuffix(got, "xxxxxxxEND") || !strings.Contains(got, "omitted") {
		t.Fatalf("result tail %q", got)
	}
	if _, err := continueDelegatedTask(task.ID, "more"); err == nil || !strings.Contains(err.Error(), "no agent conversation") {
		t.Fatalf("continue without session: %v", err)
	}
	t.Setenv("JARA_MAX_DELEGATED_TASKS", "0")
	if _, err := startDelegatedTask(delegateArguments{Agent: "codex", Prompt: "hi", CWD: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("task cap not enforced: %v", err)
	}
	if got := envLimit("JARA_MAX_DELEGATED_TASKS", 4, 50); got != 0 {
		t.Fatalf("env limit %d", got)
	}
	if workspaceForPath(filepath.Join(fsutil.DataRoot(), "workspaces", "w-abc", "src")) != "w-abc" || workspaceForPath("/tmp") != "" {
		t.Fatal("workspace mapping")
	}
}

func TestHandoffPromptAndTarget(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	repo := gitFixture(t)
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("changed\n"), 0600)
	prompt := handoffPrompt(repo, protocol.AgentCodex, protocol.AgentClaude, "finish the parser")
	for _, want := range []string{"from Codex", "finish the parser", "M a.txt", "init", "You are Claude Code"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("handoff prompt lacks %q:\n%s", want, prompt)
		}
	}
	command, err := agentTerminalCommand(Request{Runtime: protocol.AgentClaude, Handoff: true, Prompt: "p"}, []string{"--add-dir", "x"})
	if err != nil || !slices.Equal(command, []string{protocol.ClaudeBinary, "--add-dir", "x", "--", "p"}) {
		t.Fatalf("handoff command %q, %v", command, err)
	}
	if got := sessionMenuLabel(Request{Handoff: true, Runtime: protocol.AgentCodex}); got != "Handoff to Codex" {
		t.Fatalf("label %q", got)
	}
}

func TestResolveClientAgent(t *testing.T) {
	c := Config{Agent: protocol.AgentCodex, Remotes: map[string]Remote{"work": {Agent: protocol.AgentClaude}}, WorkspaceAgents: map[string]string{"/repo": protocol.AgentCodex}}
	cases := []struct{ root, remote, action, explicit, want string }{
		{"/repo", "work", "open", "", protocol.AgentCodex},
		{"/other", "work", "open", "", protocol.AgentClaude},
		{"/other", "home", "open", "", protocol.AgentCodex},
		{"/repo", "work", "open", "claude", protocol.AgentClaude},
		{"/repo", "work", "handoff", "", ""},
	}
	for _, c2 := range cases {
		got, err := resolveClientAgent(c, c2.root, c2.remote, c2.action, c2.explicit)
		if err != nil || got != c2.want {
			t.Fatalf("%+v: %q %v", c2, got, err)
		}
	}
	if validateAgentFlags("open", "", "codex", "note") == nil {
		t.Fatal("--note needs handoff")
	}
}
