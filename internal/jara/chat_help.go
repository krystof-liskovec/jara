package jara

import "strings"

// /help lists the chat commands by topic, as markdown the conversation view
// styles: a heading per topic and one bullet per command, so long lines wrap
// under their own text.

type chatHelpEntry struct{ command, args, about string }

var chatHelpSections = []struct {
	title   string
	entries []chatHelpEntry
}{
	{"Conversations", []chatHelpEntry{
		{"/new", "", "Start a new conversation"},
		{"/clear", "", "Start a new conversation with an empty transcript"},
		{"/sessions", "[query]", "Browse saved sessions in this folder"},
		{"/resume", "[N or ID]", "Browse recent sessions or resume one"},
		{"/rewind", "[N]", "Go back to before prompt N and edit it; files are not reverted"},
		{"/export", "[PATH]", "Save the conversation as Markdown"},
		{"/compose", "", "Write a multi-line message"},
		{"/send", "", "Send the composed message"},
		{"/discard", "", "Discard the composed message"},
		{"/vim", "[on or off or status]", "Toggle Vim editing in this terminal's composer (F7)"},
	}},
	{"Models", []chatHelpEntry{
		{"/models", "", "Browse and select models, by provider"},
		{"/model", "[ID or N]", "Browse models or select one directly"},
		{"/reasoning", "[EFFORT]", "Select the reasoning effort"},
		{"/default", "[ID or N]", "Set the default model and provider for new conversations"},
		{"/fallback", "[ID or N or off]", "Continue on another model when a usage limit is reached"},
	}},
	{"During a turn", []chatHelpEntry{
		{"/cancel", "", "Stop the current turn"},
		{"/approve", "", "Allow the oldest pending request"},
		{"/deny", "", "Reject the oldest pending request"},
		{"/answer", "N X", "Answer question N with option X or text"},
		{"/queue", "[run or clear or edit N]", "Inspect or manage queued prompts"},
		{"/btw", "QUESTION", "Ask a side question; works during a turn and is not saved"},
	}},
	{"Working modes", []chatHelpEntry{
		{"/plan", "[PROMPT or off or status]", "Use Plan mode for this and later turns"},
		{"/auto", "[on or off or status]", "Let an automatic reviewer handle approvals (on by default)"},
		{"/goal", "[ARGS]", "Set or manage a persistent goal for this conversation"},
		{"/compact", "[idle ...]", "Compact now; /compact idle on, off or DURATION sets idle compaction"},
		{"/crunch", "PROMPT", "Work until done, waiting out usage limits; /crunch status or stop manages it"},
		{"/goalcrunch", "OBJECTIVE", "Like /crunch, until the goal is marked complete"},
		{"/schedule", "in 30m PROMPT", "Run a prompt later, also when you are away; /schedule list or cancel ID manages them"},
	}},
	{"Workspace", []chatHelpEntry{
		{"/diff", "", "Show or hide the changes pane"},
		{"/image", "PATH", "Attach an image; Ctrl+V pastes one from the clipboard"},
		{"/agents", "[N]", "List subagents or view subagent N; Ctrl+↓/↑ switches, Esc returns"},
		{"!COMMAND", "", "Run a shell command in this workspace"},
		{"@PATH", "", "Mention a workspace file; typing @ lists matches"},
	}},
	{"Session", []chatHelpEntry{
		{"/status", "", "Show connection, runtime and turn state"},
		{"/alerts", "[on or off]", "Bell and desktop alerts for long turns and pending requests"},
		{"/notify", "[URL or off or test]", "Push notifications to a phone (ntfy.sh topic or webhook)"},
		{"/accounts", "[use N or remove N]", "List the pooled accounts; Jara switches to the next when one runs out of usage"},
		{"/login", "[codex or claude [METHOD] or cancel]", "Sign Codex or Claude Code in where the agent runs"},
		{"/usage", "[REMOTE]", "Show the usage limits of the Codex and Claude Code accounts here and on every remote"},
		{"/mcp", "[add, where, remove, import or sync]", "Show the shared MCP servers and what this conversation loaded, or change them"},
		{"/remote", "[NAME or here]", "Move this conversation to this device or a remote host"},
		{"/detach", "", "Disconnect and leave the remote session running"},
		{"/quit", "", "Stop idle sessions; leave active agents running"},
		{"/stop", "", "Stop the remote session and release the workspace"},
		{"/help", "", "Show this list"},
	}},
}

var chatHelpKeys = []string{
	"`Enter` sends; while a turn runs, the message joins that turn",
	"`Tab` while a turn runs queues the message for after it",
	"`F2` and `F3` open the model and reasoning menus; `Shift+↑/↓` raises or lowers reasoning",
	"`Shift+Tab` toggles auto mode",
	"`Esc` closes menus or enters Vim Normal mode. With Vim off and nothing open, it stops a running turn",
	"`Ctrl+C` stops a running turn; on an idle one it closes the session and the next `jara` starts a new conversation (`jara resume --latest` returns)",
	"`Ctrl+D` on an empty input detaches: the session keeps running and the next `jara` comes back to it",
	"`←` on an empty input opens recent sessions; in menus `→` chooses and `←` goes back",
	"`Shift`+drag selects text to copy with your terminal",
	"Prompt files in ~/.codex/prompts, ~/.claude/commands and the project's .claude/commands work as slash commands with either provider",
}

func (u *chatUI) help() string {
	var b strings.Builder
	for _, section := range chatHelpSections {
		b.WriteString("### " + section.title + "\n")
		for _, entry := range section.entries {
			command := entry.command
			if entry.args != "" {
				command += " " + entry.args
			}
			b.WriteString("- `" + command + "` — " + entry.about + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("### Keys\n")
	for _, key := range chatHelpKeys {
		b.WriteString("- " + key + "\n")
	}
	return b.String()
}
