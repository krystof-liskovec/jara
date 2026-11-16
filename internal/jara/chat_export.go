package jara

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// /export saves the conversation as Markdown; /diff lists the file changes
// this conversation made and brings back the changes pane.

func (u *chatUI) runChatExportCommand(args string) {
	transcript, ok := u.out.(interface{ ChatMarkdown() string })
	if !ok {
		u.print("Export is available in the interactive Jara interface.\n")
		return
	}
	markdown := transcript.ChatMarkdown()
	if strings.TrimSpace(markdown) == "" {
		u.printRole("system", "There is nothing to export yet.\n")
		return
	}
	path := chatPastedPath(args)
	if path == "" {
		path = "jara-conversation-" + time.Now().Format("20060102-150405") + ".md"
	}
	if !filepath.IsAbs(path) {
		base := u.cwd
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			base, _ = os.Getwd() // the workspace lives on another host
		}
		path = filepath.Join(base, path)
	}
	header := "# Jara conversation\n\n"
	if u.thread != "" {
		header += "Conversation `" + u.thread + "` · exported " + time.Now().Format("2006-01-02 15:04") + "\n\n"
	}
	if err := os.WriteFile(path, []byte(header+markdown), 0o644); err != nil {
		u.printRole("system", "Could not export the conversation: "+err.Error()+"\n")
		return
	}
	u.printRole("system", "Exported the conversation to "+path+"\n")
}

// ChatMarkdown renders the transcript: prompts and replies as sections,
// command output as code; progress and system notes are left out.
func (o *chatTerminalOutput) ChatMarkdown() string {
	var b strings.Builder
	for _, entry := range o.entries {
		text := entry.text.String()
		if entry.tool != nil {
			text = entry.tool.title + "\n" + text
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		stamp := ""
		if !entry.at.IsZero() {
			stamp = " · " + entry.at.Format("15:04")
		}
		switch entry.role {
		case "user":
			fmt.Fprintf(&b, "## You%s\n\n%s\n\n", stamp, text)
		case "assistant":
			fmt.Fprintf(&b, "## %s%s\n\n%s\n\n", textutil.FirstNonEmpty(entry.agent, "Assistant"), stamp, text)
		case "tool":
			if strings.Contains(text, "```") {
				fmt.Fprintf(&b, "%s\n\n", text)
			} else {
				fmt.Fprintf(&b, "```text\n%s\n```\n\n", text)
			}
		case "subagent", "btw":
			fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(text, "\n", "\n> "))
		}
	}
	if o.trimmed {
		return "_Earlier messages were trimmed from this session's transcript._\n\n" + b.String()
	}
	return b.String()
}

// ClearChat empties the transcript for /clear and /rewind.
func (o *chatTerminalOutput) ClearChat() {
	o.entries, o.bytes, o.trimmed = nil, 0, false
	o.viewLines, o.viewDirty = nil, true
}

func (u *chatUI) runChatDiffCommand() {
	u.refreshGitDiff()
	if u.diff.togglePane() {
		return
	}
	if u.diff.git.available {
		if len(u.diff.git.changes) == 0 {
			u.printRole("system", "No uncommitted changes.\n")
			return
		}
		u.printRole("tool", "\nUncommitted changes:\n"+formatChatFileChangeList("", u.diff.git.changes))
		return
	}
	if len(u.diff.changes) == 0 {
		u.printRole("system", "No file changes in this conversation yet.\n")
		return
	}
	changes := make([]protocol.FileChange, len(u.diff.changes))
	for index, change := range u.diff.changes {
		changes[len(changes)-1-index] = change // oldest first
	}
	u.printRole("tool", "\n"+formatChatFileChangeList("", changes))
	if u.diff.mode == chatDiffPaneForcedOff {
		u.diff.mode = chatDiffPaneAuto
	}
	if len(u.diff.changes) == chatDiffPaneKeepFiles {
		u.printRole("system", fmt.Sprintf("Showing the %d most recently changed files.\n", chatDiffPaneKeepFiles))
	}
}
