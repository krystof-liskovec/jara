//go:build linux || darwin

package jara

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

func phoneTranscript(width int) []chatViewLine {
	out := &chatTerminalOutput{}
	out.WriteChatRole("user", "fix the login test\n")
	out.WriteChatRole("assistant", "Done:\n```go\nif time.Now().After(token.Expiry) { return ErrExpired }\n```\nTests pass.\n")
	out.setWidth(width - 4)
	return out.transcriptLines(width - 4)
}

func TestShortScreensKeepTheConversationVisible(t *testing.T) {
	model := chatViewModel{TranscriptLines: phoneTranscript(44),
		ControlModel: "GPT-5.5", ControlReasoning: "high", ControlContext: "82% left", Hints: []string{"Shift+drag select", "Enter send"}}
	frame := renderChatView(model, 44, 14, menuColorsForTerminal(), false)
	screen := strings.Join(frame.Lines, "\n")
	// No header: the conversation starts on the first row.
	if !strings.Contains(frame.Lines[0], "You") || strings.Contains(screen, "Shift+drag") || strings.Contains(screen, "(F2)") {
		t.Fatalf("short screen chrome:\n%s", screen)
	}
	if !strings.Contains(frame.Lines[13], "Enter send · GPT-5.5 · high · 82% left") || frame.target(13, 16) != "model" {
		t.Fatalf("controls in the border: %q targets %+v", frame.Lines[13], frame.Targets)
	}
	if !strings.Contains(screen, "Tests pass.") || strings.Contains(screen, "\n \n \n") {
		t.Fatalf("blank runs are collapsed:\n%s", screen)
	}
	if !strings.Contains(screen, "…") {
		t.Fatalf("long code lines are cut on narrow screens:\n%s", screen)
	}
	model.ApprovalPending = true
	frame = renderChatView(model, 44, 14, menuColorsForTerminal(), false)
	if row := frame.Lines[10]; !strings.Contains(row, "[ ⏎/y Allow ]  [ n Deny ]") || frame.target(10, strings.Index(row, "[ n")) != "deny" {
		t.Fatalf("approval row %q %+v", row, frame.Targets)
	}
	tall := renderChatView(model, 44, 30, menuColorsForTerminal(), false)
	if footer := tall.Lines[29]; strings.Contains(footer, "Shift+drag") || !strings.Contains(footer, `\ Enter newline`) {
		t.Fatalf("phone hints %q", footer)
	}
}

func TestTapsOpenPickersAndAnswerApprovals(t *testing.T) {
	term, wire := newTestChatTerminal(t)
	term.u.models = newChatModelBrowser()
	term.u.models.complete = true
	term.targets = chatFrame{Targets: []chatTapTarget{{row: 3, from: 2, to: 10, action: "reasoning"}, {row: 5, from: 0, to: 20, action: "approve"}}}
	typeChatKeys(t, term, "\x1b[<0;5;4M\x1b[<0;5;4m")
	if term.picker.kind != "reasoning" {
		t.Fatalf("tapping the reasoning label opens its picker, got %q", term.picker.kind)
	}
	term.picker.close()
	term.u.approvals = []appMessage{{ID: json.RawMessage(`"a1"`)}}
	typeChatKeys(t, term, "\x1b[<0;3;6M")
	if len(term.u.approvals) != 0 || !strings.Contains(wire.String(), `"decision":"accept"`) {
		t.Fatalf("tapping Allow approves: %s", wire.String())
	}
	term.u.approvals = []appMessage{{ID: json.RawMessage(`"a2"`)}}
	typeChatKeys(t, term, "n")
	if len(term.u.approvals) != 0 || !strings.Contains(wire.String(), `"decision":"decline"`) || term.editor.Text() != "" {
		t.Fatalf("n denies: %s", wire.String())
	}
	typeChatKeys(t, term, "n")
	if term.editor.Text() != "n" {
		t.Fatal("without a pending approval, n is just text")
	}
	typeChatKeys(t, term, "\x1b[Z")
	if !term.u.manualApprovals || term.editor.Text() != "n" {
		t.Fatal("Shift+Tab turns auto mode off without touching the input")
	}
	term.targets = chatFrame{Targets: []chatTapTarget{{row: 3, from: 2, to: 10, action: "auto"}}}
	typeChatKeys(t, term, "\x1b[<0;5;4M\x1b[<0;5;4m")
	if term.u.manualApprovals {
		t.Fatal("tapping the auto mode control turns it back on")
	}
}

func TestBackslashEnterInsertsANewline(t *testing.T) {
	editor := newChatEditor(100)
	editor.SetText(`first line\`)
	if result := editor.Apply(chatEditorInput{Key: chatEditorEnter}); result.Submitted || editor.Text() != "first line\n" {
		t.Fatalf("got %q submitted=%v", editor.Text(), result.Submitted)
	}
	editor.Apply(chatEditorInput{Key: chatEditorText, Text: "second"})
	if result := editor.Apply(chatEditorInput{Key: chatEditorEnter}); !result.Submitted || result.Value != "first line\nsecond" {
		t.Fatalf("send %+v", result)
	}
}

func TestPushNotifications(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	var mu sync.Mutex
	var received []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, r.Header.Get("Title")+"|"+r.Header.Get("Priority")+"|"+string(body))
		mu.Unlock()
	}))
	defer server.Close()
	if _, err := handleChatNotifyRequest("jara/notify/set", json.RawMessage(`{"url":"ftp://x"}`), "demo"); err == nil {
		t.Fatal("non-http URLs are refused")
	}
	result, err := handleChatNotifyRequest("jara/notify/set", json.RawMessage(`{"url":"`+server.URL+`"}`), "demo")
	if err != nil || result.(map[string]any)["url"] != server.URL {
		t.Fatalf("set %v %v", result, err)
	}
	if _, err = os.Stat(filepath.Join(fsutil.DataRoot(), "notify.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = handleChatNotifyRequest("jara/notify/test", nil, "demo"); err != nil {
		t.Fatal(err)
	}
	notifier := &chatPushNotifier{turnStarted: map[string]time.Time{}, post: postChatPush}
	event := func(method, params string) {
		notifier.observe(appMessage{Method: method, Params: json.RawMessage(params)}, "demo")
	}
	event("turn/started", `{"turn":{"id":"quick"}}`)
	event("turn/completed", `{"turn":{"id":"quick","status":"completed"}}`)
	notifier.turnStarted["long"] = time.Now().Add(-time.Minute)
	event("turn/completed", `{"turn":{"id":"long","status":"completed"}}`)
	event("item/commandExecution/requestApproval", `{}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		count := len(received)
		mu.Unlock()
		if count >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(received, "\n")
	if len(received) != 3 || !strings.Contains(joined, "Jara · demo|default|Test notification") || !strings.Contains(joined, "|default|Turn finished") || !strings.Contains(joined, "|high|Approval needed") {
		t.Fatalf("notifications:\n%s", joined)
	}
	if _, err = handleChatNotifyRequest("jara/notify/set", json.RawMessage(`{"url":""}`), "demo"); err != nil || loadChatPushConfig().URL != "" {
		t.Fatal("off clears the URL")
	}
}

func TestFileChangesListEveryFileBeforeTheDiffs(t *testing.T) {
	text := formatChatFileChangeList("", []protocol.FileChange{{Path: "a.go", Diff: "@@ -1 +1 @@\n-a\n+b"}, {Path: "b.go", Diff: "@@ -1 +1 @@\n-c\n+d"}})
	if first, second := strings.Index(text, "- b.go"), strings.Index(text, "```diff a.go"); first < 0 || second < 0 || first > second {
		t.Fatalf("summary first:\n%s", text)
	}
}
