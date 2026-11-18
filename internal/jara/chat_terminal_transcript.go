package jara

import (
	"strings"
	"time"
	"unicode/utf8"
)

const (
	chatTranscriptLimit     = 512 * 1024
	chatTranscriptTrimSlack = 32 * 1024
)

// chatTerminalOutput formats completed transcript lines once as streamed text arrives.
type chatTerminalOutput struct {
	entries []*chatTerminalEntry
	bytes   int
	trimmed bool

	width     int
	agent     string
	viewDirty bool
	viewLines []chatViewLine

	expanded map[string]bool // tool calls opened with a click; kept across ClearChat
	toolSeq  int             // names tool calls that came without an ID
}

type chatTerminalEntry struct {
	role    string
	agent   string // agent label for assistant and progress headers
	at      time.Time
	text    strings.Builder
	bytes   int
	lines   []chatViewLine
	pending strings.Builder
	hasLine bool
	inCode  string        // open code fence info, "" outside code
	tool    *chatToolCall // set when the entry is one tool call
}

func (o *chatTerminalOutput) Write(data []byte) (int, error) {
	text := string(data)
	text = strings.ReplaceAll(text, "You> ", "")
	text = strings.ReplaceAll(text, "Compose> ", "")
	text = strings.TrimPrefix(text, "Jara conversation · /help for commands · Ctrl-C cancels a turn; while idle it stops the session\n")
	if last := len(o.entries) - 1; strings.TrimSpace(text) == "" && (last < 0 || o.entries[last].role != "system" || o.entries[last].tool != nil) {
		return len(data), nil // a blank line, such as after "You> ", is no message
	}
	o.append("system", text)
	return len(data), nil
}

func (o *chatTerminalOutput) WriteChatRole(role, text string) {
	o.appendAt(role, text, time.Now())
}

func (o *chatTerminalOutput) WriteChatRoleAt(role, text string, at time.Time) {
	o.appendAt(role, text, at)
}

func (o *chatTerminalOutput) InteractiveChat() bool { return true }

// SetChatAgent labels following assistant output with the active agent.
func (o *chatTerminalOutput) SetChatAgent(agent string) { o.agent = agent }

func (o *chatTerminalOutput) append(role, text string) {
	o.appendAt(role, text, time.Now())
}

func (o *chatTerminalOutput) appendAt(role, text string, at time.Time) {
	text = chatText(text)
	if text == "" {
		return
	}
	var entry *chatTerminalEntry
	if count := len(o.entries); count > 0 && o.entries[count-1].role == role && o.entries[count-1].agent == o.agent && o.entries[count-1].tool == nil {
		entry = o.entries[count-1]
	} else {
		entry = &chatTerminalEntry{role: role, agent: o.agent, at: at}
		o.entries = append(o.entries, entry)
	}
	o.appendTo(entry, text)
}

func (o *chatTerminalOutput) appendTo(entry *chatTerminalEntry, text string) {
	if text == "" {
		return
	}
	if len(text) > chatTranscriptLimit {
		text, _ = truncateChatGraphemes(text, chatTranscriptLimit)
		o.trimmed = true
	}
	entry.text.WriteString(text)
	entry.bytes += len(text)
	o.bytes += len(text)
	entry.appendVisual(text, max(1, o.width))
	o.enforceLimit()
	o.viewDirty = true
}

func (o *chatTerminalOutput) setWidth(width int) {
	width = max(1, width)
	if o.width == width {
		return
	}
	o.width = width
	for _, entry := range o.entries {
		entry.reflow(width)
	}
	o.viewDirty = true
}

func (e *chatTerminalEntry) appendVisual(text string, width int) {
	for len(text) > 0 {
		newline := strings.IndexByte(text, '\n')
		if newline < 0 {
			e.pending.WriteString(text)
			e.hasLine = true
			return
		}
		e.pending.WriteString(text[:newline])
		e.hasLine = true
		lines, inCode := e.expandLine(e.pending.String(), width)
		e.lines = append(e.lines, lines...)
		e.inCode = inCode
		e.pending.Reset()
		e.hasLine = true
		text = text[newline+1:]
	}
}

func (e *chatTerminalEntry) reflow(width int) {
	text := e.text.String()
	e.lines = nil
	e.pending.Reset()
	e.hasLine = false
	e.inCode = ""
	e.appendVisual(text, width)
}

// bodyLines renders an entry's text without blank rows at its start or end:
// the gap between messages already spaces them, and a reply that opens with
// a paragraph break would otherwise start with empty rows.
func (e *chatTerminalEntry) bodyLines(width int) []chatViewLine {
	rows := e.lines
	if e.hasLine {
		pending, _ := e.expandLine(e.pending.String(), width)
		rows = append(rows[:len(rows):len(rows)], pending...)
	}
	for len(rows) > 0 && chatBlankRow(rows[0]) {
		rows = rows[1:]
	}
	for len(rows) > 0 && chatBlankRow(rows[len(rows)-1]) {
		rows = rows[:len(rows)-1]
	}
	return rows
}

func chatBlankRow(line chatViewLine) bool {
	return line.kind == "body" && strings.TrimSpace(line.text) == ""
}

func (o *chatTerminalOutput) enforceLimit() {
	if o.bytes <= chatTranscriptLimit+chatTranscriptTrimSlack {
		return
	}
	for o.bytes > chatTranscriptLimit {
		if len(o.entries) > 1 {
			o.bytes -= o.entries[0].bytes
			o.entries[0] = nil
			o.entries = o.entries[1:]
			o.trimmed = true
			continue
		}
		if len(o.entries) == 0 {
			return
		}
		entry := o.entries[0]
		text := entry.text.String()
		trim := len(text) - chatTranscriptLimit
		for trim < len(text) && !utf8.RuneStart(text[trim]) {
			trim++
		}
		text = text[trim:]
		o.bytes -= trim
		entry.bytes -= trim
		entry.text.Reset()
		entry.text.WriteString(text)
		entry.reflow(max(1, o.width))
		o.trimmed = true
		break
	}
}

func (o *chatTerminalOutput) transcriptLines(width int) []chatViewLine {
	o.setWidth(width)
	if !o.viewDirty {
		return o.viewLines
	}
	lines := make([]chatViewLine, 0, 2+len(o.entries)*2)
	previousRole := ""
	if o.trimmed {
		lines = append(lines,
			chatViewLine{text: "• Jara", kind: "role-system"},
			chatViewLine{text: "  Earlier output was trimmed to keep this session responsive.", kind: "body"},
		)
		previousRole = "system"
	}
	previousAgent := ""
	for _, entry := range o.entries {
		// Entries of one message share its header: tool calls in a row, or
		// a message around a folded row. Other entries of a role always
		// start a new message, since text of the same role joins the last.
		continued := entry.role == previousRole && (entry.role == "tool" || entry.agent == previousAgent)
		if !continued && chatViewNeedsMessageGap(previousRole, entry.role) {
			lines = append(lines, chatViewLine{kind: "message-gap"})
		}
		if !continued {
			header := expandChatTranscript([]chatViewMessage{{Role: entry.role, At: entry.at, Agent: entry.agent}}, width)
			lines = append(lines, header...)
		}
		previousRole, previousAgent = entry.role, entry.agent
		if entry.tool != nil {
			lines = append(lines, o.toolLines(entry, width)...)
			continue
		}
		lines = append(lines, entry.bodyLines(width)...)
	}
	o.viewLines = lines
	o.viewDirty = false
	return o.viewLines
}
