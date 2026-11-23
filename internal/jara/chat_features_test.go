package jara

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rivo/uniseg"
	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/protocol"
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
	// A model or reasoning switch shows just the new choice.
	u := &chatUI{out: &strings.Builder{}, effectiveReasoning: "high", nextReasoning: "low"}
	if got := u.reasoningControlLabel(); got != "Low" {
		t.Fatalf("switched reasoning %q", got)
	}
	u.nextModel, u.nextModelLabel = "gpt-6", "GPT-6"
	if got := u.modelControlLabel(); got != "GPT-6" {
		t.Fatalf("switched model %q", got)
	}
}

func TestControlsNameTheHostRunningTheAgent(t *testing.T) {
	model := chatViewModel{ControlModel: "GPT-5.5", ControlReasoning: "High", ControlContext: "75% left"}
	if screen := strings.Join(renderChatView(model, 120, 20, menuColorsForTerminal(), false).Lines, "\n"); strings.Contains(screen, "⇄") {
		t.Fatalf("on this device no host shows:\n%s", screen)
	}
	model.ControlHost = "jellyfish"
	frame := renderChatView(model, 120, 20, menuColorsForTerminal(), false)
	row := len(frame.Lines) - 1
	line := frame.Lines[row]
	if !strings.Contains(line, "◔ Context 75% left  ⇄ jellyfish  ⏵⏵ Auto mode on") || frame.target(row, uniseg.StringWidth(line[:strings.Index(line, "jellyfish")])) != "remote" {
		t.Fatalf("controls %q, targets %+v", line, frame.Targets)
	}
	// On a short phone screen it rides in the input border with the rest.
	short := renderChatView(model, 60, 12, menuColorsForTerminal(), false)
	if bottom := short.Lines[len(short.Lines)-1]; !strings.Contains(bottom, "⇄ jellyfish") {
		t.Fatalf("short screen border %q", bottom)
	}
}

func TestMenusHaveTheirOwnBackground(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	palette := menuColorsForBackground(terminalRGB{r: 30, g: 30, b: 30})
	model := chatViewModel{ControlModel: "GPT-5.5", ControlReasoning: "High",
		Messages:    []chatViewMessage{{Role: "assistant", Text: "Done."}},
		Suggestions: []chatCompletionChoice{{Value: "gpt-5.5", Index: 0, Total: 2, Selected: true}, {Value: "gpt-6", Index: 1, Total: 2}}}
	frame := renderChatView(model, 80, 20, palette, true)
	rowWith := func(text string) string {
		for _, line := range frame.Lines {
			if strings.Contains(line, text) {
				return line
			}
		}
		t.Fatalf("no row with %q", text)
		return ""
	}
	if row := rowWith("gpt-6"); !strings.HasPrefix(row, menuBG(palette.surface)) {
		t.Fatalf("menu row %q", row)
	}
	if row := rowWith("gpt-5.5"); !strings.HasPrefix(row, menuBG(palette.selection)) {
		t.Fatalf("selected row %q", row)
	}
	if row := rowWith("Done."); !strings.HasPrefix(row, menuBG(palette.canvas)) || palette.surface == palette.canvas {
		t.Fatalf("conversation row %q", row)
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

func TestChatMentionBrowsesFolders(t *testing.T) {
	u := &chatUI{}
	key := chatMentionFolderKey("/work", ".config/")
	u.mentions.folderRequested = key
	result, _ := json.Marshal(map[string]any{"entries": []map[string]any{
		{"name": "claude", "dir": true}, {"name": "Code", "dir": true}, {"name": "has space", "dir": true}, {"name": ".hidden"}, {"name": "cava.conf"},
	}})
	u.acceptChatFolderResponse(appMessage{Result: result}, chatPending{action: key})
	context := chatCompletionContextFromUI(u)
	if !context.FolderListed || context.Folder != ".config/" || len(context.FolderEntries) != 4 {
		t.Fatalf("context: %+v", context)
	}
	text := "I have a script in @.config/"
	candidates := chatCompletionCandidates(text, len(text), context)
	if len(candidates) != 3 || candidates[0].replacement != "@.config/claude/" || !candidates[0].keepOpen || candidates[0].append != "" {
		t.Fatalf("folder candidates: %+v", candidates)
	}
	if hidden := chatCompletionCandidates("@.config/.", 10, context); len(hidden) != 1 || hidden[0].replacement != "@.config/.hidden" {
		t.Fatalf("hidden entries: %+v", hidden)
	}
	if prefixed := chatCompletionCandidates("@.config/c", 10, context); len(prefixed) != 3 {
		t.Fatalf("prefix is case-insensitive: %+v", prefixed)
	}
	editor := newChatEditor(1024)
	editor.SetCompletionContext(context)
	editor.SetText(text)
	editor.Apply(chatEditorInput{Key: chatEditorTab})
	if editor.Text() != "I have a script in @.config/claude/" || editor.dismissed {
		t.Fatalf("folder applied: %q dismissed=%v", editor.Text(), editor.dismissed)
	}
	// The top level is listed for a bare @, before the fuzzy search answers.
	top := chatCompletionContext{Folder: "", FolderListed: true, FolderEntries: []chatCompletionOption{{Value: ".config/", Description: "folder"}, {Value: "README.md", Description: "file"}}}
	if bare := chatCompletionCandidates("@", 1, top); len(bare) != 1 || bare[0].replacement != "@README.md" {
		t.Fatalf("bare @: %+v", bare)
	}
	if dot := chatCompletionCandidates("@.co", 4, top); len(dot) != 1 || dot[0].replacement != "@.config/" {
		t.Fatalf("hidden top level: %+v", dot)
	}
}
