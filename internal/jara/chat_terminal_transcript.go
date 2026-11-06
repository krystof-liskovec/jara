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
	viewDirty bool
	viewLines []chatViewLine
}

type chatTerminalEntry struct {
	role    string
	at      time.Time
	text    strings.Builder
	bytes   int
	lines   []chatViewLine
	pending strings.Builder
	hasLine bool
	inCode  bool
}

func (o *chatTerminalOutput) Write(data []byte) (int, error) {
	text := string(data)
	text = strings.ReplaceAll(text, "You> ", "")
	text = strings.ReplaceAll(text, "Compose> ", "")
	if strings.HasPrefix(text, "Jara conversation · /help for commands · Ctrl-C cancels a turn; while idle it stops the session\n") {
		text = strings.TrimPrefix(text, "Jara conversation · /help for commands · Ctrl-C cancels a turn; while idle it stops the session\n")
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

func (o *chatTerminalOutput) append(role, text string) {
	o.appendAt(role, text, time.Now())
}

func (o *chatTerminalOutput) appendAt(role, text string, at time.Time) {
	text = chatText(text)
	if text == "" {
		return
	}
	if len(text) > chatTranscriptLimit {
		text, _ = truncateChatGraphemes(text, chatTranscriptLimit)
		o.trimmed = true
	}
	var entry *chatTerminalEntry
	if count := len(o.entries); count > 0 && o.entries[count-1].role == role {
		entry = o.entries[count-1]
	} else {
		entry = &chatTerminalEntry{role: role, at: at}
		o.entries = append(o.entries, entry)
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
		lines, inCode := expandChatTranscriptLine(e.pending.String(), width, e.role, e.inCode)
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
	e.inCode = false
	e.appendVisual(text, width)
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
	for _, entry := range o.entries {
		if chatViewNeedsMessageGap(previousRole, entry.role) {
			lines = append(lines, chatViewLine{kind: "message-gap"})
		}
		header := expandChatTranscript([]chatViewMessage{{Role: entry.role, At: entry.at}}, width)
		lines = append(lines, header...)
		lines = append(lines, entry.lines...)
		if entry.hasLine {
			pending, _ := expandChatTranscriptLine(entry.pending.String(), width, entry.role, entry.inCode)
			lines = append(lines, pending...)
		}
		previousRole = entry.role
	}
	o.viewLines = lines
	o.viewDirty = false
	return o.viewLines
}
