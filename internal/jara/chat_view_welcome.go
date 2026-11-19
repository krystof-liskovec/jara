package jara

import (
	"path/filepath"
	"strings"
)

// chatWelcome personalizes the screen a conversation without messages shows.
type chatWelcome struct {
	Workspace string // the project's name; "" says "this project"
	Note      string // where the agent runs and who keeps the conversation
}

var chatWelcomeHints = [][2]string{
	{"/resume", "recent sessions"}, {"/help", "command guide"},
	{"/model", "switch model"}, {"/new", "fresh conversation"},
}

func chatWelcomeTranscript(rows, width int, mark string, welcome chatWelcome) []chatViewLine {
	if mark == "" {
		mark = "✦"
	}
	project := "this project"
	if name := strings.TrimSpace(welcome.Workspace); name != "" {
		project = name
	}
	content := []chatViewLine{
		{text: mark, kind: "welcome-mark"},
		{text: "Ready when you are", kind: "welcome-title"},
		{text: "Ask Codex or Claude to explore or change " + project + ".", kind: "welcome-copy"},
	}
	if note := strings.Join(strings.Fields(welcome.Note), " "); note != "" {
		content = append(content, chatViewLine{kind: "welcome-space"})
		for _, line := range wrapChatWords(note, min(width, 64), min(width, 64)) {
			content = append(content, chatViewLine{text: line, kind: "welcome-note"})
		}
	}
	content = append(content, chatViewLine{kind: "welcome-space"})
	content = append(content, chatWelcomeHintLines(width)...)
	top := max(0, (rows-len(content))/2)
	lines := make([]chatViewLine, top, max(rows, top+len(content)))
	for i := range lines {
		lines[i].kind = "welcome-space"
	}
	for _, line := range content {
		if line.text != "" {
			line.text = centerChatViewText(line.text, width)
		}
		lines = append(lines, line)
	}
	for len(lines) < rows {
		lines = append(lines, chatViewLine{kind: "welcome-space"})
	}
	return lines
}

// chatWelcomeHintLines lays the hints out in two aligned columns, or one
// when the screen is narrow; every row has one width so they center as a block.
func chatWelcomeHintLines(width int) []chatViewLine {
	cell := func(hint [2]string) string { return hint[0] + strings.Repeat(" ", 9-len(hint[0])) + hint[1] }
	var rows []string
	if width >= 56 {
		for i := 0; i+1 < len(chatWelcomeHints); i += 2 {
			left := cell(chatWelcomeHints[i])
			rows = append(rows, left+strings.Repeat(" ", 28-len(left))+cell(chatWelcomeHints[i+1]))
		}
	} else {
		for _, hint := range chatWelcomeHints {
			rows = append(rows, cell(hint))
		}
	}
	block := 0
	for _, row := range rows {
		block = max(block, len(row))
	}
	lines := make([]chatViewLine, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, chatViewLine{text: row + strings.Repeat(" ", block-len(row)), kind: "welcome-hint"})
	}
	return lines
}

// welcomeWorkspace names the project on the welcome screen.
func (u *chatUI) welcomeWorkspace() string {
	if u.workspaceLabel != "" {
		return u.workspaceLabel
	}
	if u.cwd == "" {
		return ""
	}
	return filepath.Base(u.cwd)
}
