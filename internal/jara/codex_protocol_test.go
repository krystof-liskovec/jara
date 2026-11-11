package jara

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Jara depends on these parts of the Codex app-server protocol. The installed
// Codex describes its protocol with generate-json-schema, so a Codex upgrade
// that renames or drops one of them fails here instead of in a session.
var (
	codexRequestsJaraSends = []string{
		"initialize", "model/list", "thread/list", "thread/start", "thread/resume", "thread/fork",
		"thread/unsubscribe", "thread/revert", "thread/compact/start", "thread/settings/update",
		"thread/goal/get", "thread/goal/set", "thread/goal/clear", "turn/start", "turn/interrupt",
		"config/read", "config/value/write", "account/rateLimits/read", "fuzzyFileSearch",
	}
	codexNotificationsJaraReads = []string{
		"error", "thread/started", "thread/compacted", "thread/settings/updated", "thread/tokenUsage/updated",
		"thread/goal/updated", "thread/goal/cleared", "turn/started", "turn/completed", "item/started",
		"item/completed", "item/agentMessage/delta", "item/commandExecution/outputDelta",
		"item/reasoning/summaryTextDelta",
	}
	codexServerRequestsJaraAnswers = []string{
		"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/tool/requestUserInput",
	}
	// Definition properties Jara reads or writes, as "Definition.property".
	codexFieldsJaraUses = []string{
		"ThreadRevertParams.beforeTurnId", "ThreadForkParams.ephemeral", "ThreadForkParams.developerInstructions",
		"FuzzyFileSearchParams.roots", "ThreadTokenUsage.modelContextWindow", "TokenUsageBreakdown.totalTokens",
		"ThreadStartParams.approvalsReviewer", "TurnStartParams.effort", "ConfigValueWriteParams.keyPath",
	}
)

func TestCodexProtocolContract(t *testing.T) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not installed")
	}
	dir := t.TempDir()
	if output, err := exec.Command(binary, "app-server", "generate-json-schema", "--experimental", "--out", dir).CombinedOutput(); err != nil {
		t.Skipf("this Codex cannot describe its protocol: %v\n%s", err, output)
	}
	methods := func(file string) map[string]bool {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		var walk func(any)
		walk = func(value any) {
			switch node := value.(type) {
			case map[string]any:
				if method, ok := node["method"].(map[string]any); ok {
					for _, name := range asStrings(method["enum"]) {
						found[name] = true
					}
				}
				for _, child := range node {
					walk(child)
				}
			case []any:
				for _, child := range node {
					walk(child)
				}
			}
		}
		var schema any
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		walk(schema)
		return found
	}
	for file, wanted := range map[string][]string{
		"ClientRequest.json":      codexRequestsJaraSends,
		"ServerNotification.json": codexNotificationsJaraReads,
		"ServerRequest.json":      codexServerRequestsJaraAnswers,
	} {
		available := methods(file)
		for _, method := range wanted {
			if !available[method] {
				t.Errorf("Codex no longer offers %s (%s)", method, file)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "codex_app_server_protocol.v2.schemas.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Definitions map[string]struct {
			Properties map[string]any `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	for _, field := range codexFieldsJaraUses {
		definition, property, _ := strings.Cut(field, ".")
		if _, ok := bundle.Definitions[definition].Properties[property]; !ok {
			t.Errorf("Codex protocol no longer has %s", field)
		}
	}
}

func asStrings(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
