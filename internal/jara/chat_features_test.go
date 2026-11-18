package jara

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/protocol"
	"github.com/rivo/uniseg"
)

func TestChatMentionCompletion(t *testing.T) {
	text := "look at @intjar"
	start, query, ok := chatMentionToken(text, len(text))
	if !ok || start != 8 || query != "intjar" {
		t.Fatalf("token: %d %q %v", start, query, ok)
	}
	if _, _, ok := chatMentionToken("mail me@x.y", 11); ok {
		t.Fatal("an @ inside a word is not a mention")
	}
	context := chatCompletionContext{Files: chatMentionOptions(chatMentionSearch{files: []string{"internal/jara/chat.go", "README.md", "internal/"}})}
	candidates := chatCompletionCandidates(text, len(text), context)
	if len(candidates) != 1 || candidates[0].replacement != "@internal/jara/chat.go" || candidates[0].start != 8 {
		t.Fatalf("candidates: %+v", candidates)
	}
	if folders := chatCompletionCandidates("@inter", 6, context); len(folders) != 2 || folders[1].choice.Description != "folder" {
		t.Fatalf("folder candidates: %+v", folders)
	}
	editor := newChatEditor(1024)
	editor.SetCompletionContext(context)
	editor.SetText(text)
	editor.Apply(chatEditorInput{Key: chatEditorTab})
	if editor.Text() != "look at @internal/jara/chat.go " {
		t.Fatalf("applied mention: %q", editor.Text())
	}
}

func TestCustomCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	project := t.TempDir()
	write := func(path, text string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, "codex", "prompts", "review.md"), "Review $1 carefully.")
	write(filepath.Join(home, "claude", "commands", "review.md"), "personal review")
	write(filepath.Join(home, "claude", "commands", "git", "ship.md"), "---\ndescription: Ship it\nargument-hint: [branch]\n---\n\nShip $ARGUMENTS now.")
	write(filepath.Join(project, ".claude", "commands", "review.md"), "# Project review\nCheck the diff.")
	write(filepath.Join(project, ".claude", "commands", "help.md"), "shadowed by the built-in")
	params, _ := json.Marshal(map[string]string{"cwd": project})
	commands := agentrouter.LoadCustomCommands(params)
	byName := map[string]protocol.CustomCommand{}
	for _, command := range commands {
		byName[command.Name] = command
	}
	if byName["review"].Source != "project" || byName["review"].Description != "Project review" {
		t.Fatalf("project commands override personal ones: %+v", byName["review"])
	}
	if ship := byName["git:ship"]; ship.Description != "Ship it" || ship.ArgumentHint != "[branch]" || ship.Body != "Ship $ARGUMENTS now." {
		t.Fatalf("front matter: %+v", ship)
	}
	result, _ := json.Marshal(map[string]any{"commands": commands})
	u := &chatUI{}
	u.acceptCustomCommands(appMessage{Result: result})
	if expanded, ok := u.expandCustomCommand("/git:ship main fast"); !ok || expanded != "Ship main fast now." {
		t.Fatalf("expanded: %q %v", expanded, ok)
	}
	if _, ok := u.expandCustomCommand("/help"); ok {
		t.Fatal("built-in commands win over custom ones")
	}
	if got := expandCustomCommandBody("Review $1 and $2.", "a.go"); got != "Review a.go and ." {
		t.Fatalf("positional: %q", got)
	}
	if got := expandCustomCommandBody("Check the diff.", "focus on tests"); got != "Check the diff.\n\nfocus on tests" {
		t.Fatalf("unreferenced arguments are appended: %q", got)
	}
	completions := chatCompletionCandidates("/git:", 5, chatCompletionContext{Commands: u.customCommandCompletions()})
	if len(completions) != 1 || completions[0].replacement != "/git:ship" {
		t.Fatalf("custom command completion: %+v", completions)
	}
}

func TestChatImages(t *testing.T) {
	dir := t.TempDir()
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	path := filepath.Join(dir, "shot one.png")
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
	u := &chatUI{}
	placeholder, ok := u.pastedChatImage("'" + path + "'")
	if !ok || placeholder != "[Image #1] " {
		t.Fatalf("pasted path: %q %v", placeholder, ok)
	}
	if text, ok := u.pastedChatImage("just text"); ok || text != "just text" {
		t.Fatal("ordinary pastes are unchanged")
	}
	if _, err := u.attachChatImage([]byte("not an image")); err == nil {
		t.Fatal("non-images are rejected")
	}
	inputs := u.takeChatImages("see [Image #1]")
	if len(inputs) != 1 || !strings.HasPrefix(inputs[0].(map[string]any)["url"].(string), "data:image/png;base64,") || len(u.images) != 0 {
		t.Fatalf("image inputs: %v", inputs)
	}
	if chatPastedPath(`file:///tmp/a%20b.png`) != "/tmp/a b.png" || chatPastedPath(`/tmp/a\ b.png`) != "/tmp/a b.png" {
		t.Fatal("dropped paths are unquoted")
	}
}

func TestChatContextAlertsAndExport(t *testing.T) {
	u := &chatUI{thread: "t1"}
	if u.contextControlLabel() != "" {
		t.Fatal("no meter before usage is known")
	}
	u.acceptChatTokenUsage(json.RawMessage(`{"threadId":"t1","tokenUsage":{"last":{"totalTokens":64000},"modelContextWindow":256000}}`))
	u.acceptChatTokenUsage(json.RawMessage(`{"threadId":"other","tokenUsage":{"last":{"totalTokens":1}}}`))
	if got := u.contextControlLabel(); got != "75% left" {
		t.Fatalf("context label %q", got)
	}
	if got := chatAlertSequence("Jara", "Turn; done\x1b", false); got != "\a\x1b]9;Jara: Turn  done \x07" {
		t.Fatalf("alert %q", got)
	}
	if got := chatAlertSequence("J", "x", true); !strings.HasPrefix(got, "\a\x1bPtmux;\x1b\x1b]9;") || !strings.HasSuffix(got, "\x1b\\") {
		t.Fatalf("tmux alert %q", got)
	}
	var alerts []string
	u.alert = func(s string) { alerts = append(alerts, s) }
	u.alertTurnEnded("completed") // no start time: not a long turn
	u.alertUser("Approval needed")
	u.alertsOff = true
	u.alertUser("silenced")
	if len(alerts) != 1 {
		t.Fatalf("alerts: %q", alerts)
	}

	out := &chatTerminalOutput{}
	out.WriteChatRole("user", "hello\n")
	out.WriteChatRole("assistant", "hi there\n")
	out.WriteChatRole("tool", "$ ls\nfile\n")
	out.WriteChatRole("progress", "Thinking\n")
	markdown := out.ChatMarkdown()
	if !strings.Contains(markdown, "\n\nhello\n") || !strings.Contains(markdown, "hi there") || !strings.Contains(markdown, "```text\n$ ls\nfile\n```") || strings.Contains(markdown, "Thinking") {
		t.Fatalf("markdown:\n%s", markdown)
	}
	u.out, u.cwd = out, t.TempDir()
	u.runChatExportCommand("conversation.md")
	if data, err := os.ReadFile(filepath.Join(u.cwd, "conversation.md")); err != nil || !strings.Contains(string(data), "# Jara conversation") {
		t.Fatalf("export: %v %s", err, data)
	}
	out.ClearChat()
	if len(out.entries) != 0 {
		t.Fatal("clear empties the transcript")
	}
}

func TestChatControlsShowTheContextMeter(t *testing.T) {
	frame := renderChatView(chatViewModel{ControlModel: "GPT", ControlReasoning: "high", ControlContext: "75% left"}, 100, 20, menuColorsForTerminal(), false)
	if !strings.Contains(strings.Join(frame.Lines, "\n"), "◔ Context 75% left") {
		t.Fatalf("controls:\n%s", strings.Join(frame.Lines, "\n"))
	}
}

func TestModelAndReasoningShareOneControl(t *testing.T) {
	model := chatViewModel{ControlModel: "GPT-5.5", ControlReasoning: "High", ControlContext: "75% left"}
	frame := renderChatView(model, 100, 20, menuColorsForTerminal(), false)
	row := len(frame.Lines) - 1
	line := frame.Lines[row]
	if !strings.HasPrefix(line, " ◈ GPT-5.5 · High (F2/F3)  ◔ Context 75% left") || strings.Contains(line, "Reasoning") {
		t.Fatalf("controls %q", line)
	}
	if frame.target(row, strings.Index(line, "GPT")) != "model" || frame.target(row, uniseg.StringWidth(line[:strings.Index(line, "High")])) != "reasoning" {
		t.Fatalf("taps %+v on %q", frame.Targets, line)
	}
	// A change waiting for the next turn is said once.
	model.ControlModel, model.ControlReasoning = "GPT-5.5 · next turn", "Low · next turn"
	if line = renderChatView(model, 100, 20, menuColorsForTerminal(), false).Lines[row]; !strings.Contains(line, "◈ GPT-5.5 · Low · next turn (F2/F3)") {
		t.Fatalf("next turn %q", line)
	}
}

func TestChatRequestFailuresKeepTheSession(t *testing.T) {
	var output strings.Builder
	u := &chatUI{out: &output, pending: map[string]chatPending{}, threadReady: true, thread: "t1"}
	u.pending[`7`] = chatPending{method: "thread/name/set"}
	failure := appMessage{ID: json.RawMessage(`7`)}
	_ = json.Unmarshal([]byte(`{"error":{"message":"rejected"}}`), &failure)
	if err := u.response(failure); err != nil || !strings.Contains(output.String(), "thread/name/set failed: rejected") {
		t.Fatalf("a rejected request is reported, not fatal: %v %q", err, output.String())
	}
	u.busy, u.cancel = true, true
	if err := u.expireChatRequest("8", chatPending{method: "turn/interrupt"}); err != nil || u.cancel || !u.busy {
		t.Fatalf("an interrupt timeout keeps the turn: %v cancel=%v busy=%v", err, u.cancel, u.busy)
	}
	if err := u.expireChatRequest("9", chatPending{method: "initialize"}); err == nil {
		t.Fatal("a failed initialize still ends the session")
	}
}
