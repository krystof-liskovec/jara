package jara

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandApprovalShowsTheCommandOnce(t *testing.T) {
	command := "python3 - <<'EOF'\nold='''\tcall(t, `[{\"id\":\"db\"}]`)'''\nEOF\ngo test ./... 2>&1 | grep -E \"FAIL\""
	params, _ := json.Marshal(map[string]any{"threadId": "th", "turnId": "tu", "itemId": "cmd", "command": command, "cwd": "/work",
		"reason": "Rerun the tests", "networkApprovalContext": map[string]any{"host": "example.com"}})
	item, _ := json.Marshal(map[string]any{"id": "cmd", "type": "commandExecution", "status": "inProgress", "command": command})
	text := formatChatApproval(appMessage{Method: "item/commandExecution/requestApproval", Params: params}, item)
	if strings.Count(text, "grep -E") != 1 || strings.Contains(text, `\u003c`) || strings.Contains(text, "threadId") || strings.Contains(text, "turnId") {
		t.Fatalf("approval:\n%s", text)
	}
	// In a code block the backticks and quotes stay as written.
	if !strings.Contains(text, "```bash\n"+strings.ReplaceAll(command, "\t", "    ")+"\n```") {
		t.Fatalf("command not verbatim:\n%s", text)
	}
	for _, want := range []string{"**Run this command?**", "In /work", "Reason: Rerun the tests", `networkApprovalContext: {"host":"example.com"}`} {
		if !strings.Contains(text, want) {
			t.Errorf("approval lacks %q:\n%s", want, text)
		}
	}
	// The conversation view keeps the command's characters too.
	var shown []string
	for _, line := range expandChatMessage(text, 200, "system") {
		shown = append(shown, line.text)
	}
	if joined := strings.Join(shown, "\n"); !strings.Contains(joined, "`[{\"id\":\"db\"}]`") || strings.Contains(joined, "```") {
		t.Fatalf("view changed the command:\n%s", joined)
	}
}
