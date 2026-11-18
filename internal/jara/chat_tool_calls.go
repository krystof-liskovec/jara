package jara

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rivo/uniseg"
)

// chatToolCall makes a transcript entry one row for a command or file
// change; a click or tap on the row shows the output below it.
type chatToolCall struct {
	key        string // the item's ID; it keeps the row open across redraws
	title      string // the row's text, such as "$ go test ./..."
	status     string // "exit 1", "failed" or "declined"; empty when it went fine
	countLines bool   // the row tells how many lines of output it holds
}

// chatToolWriter is the transcript that folds tool calls into rows; other
// outputs print them in full.
type chatToolWriter interface {
	StartChatTool(key, title, body string, countLines bool, at time.Time)
	WriteChatToolOutput(key, text string)
	FinishChatTool(key, status string)
}

// startCommandCall shows a command the agent runs; its output follows.
func (u *chatUI) startCommandCall(key, command string, at time.Time) {
	title, rest := chatCommandTitle(command)
	u.startToolCall(key, title, rest, true, "\n$ "+command+"\n", at)
}

// startToolCall adds a tool call; plain is its text for outputs that show
// everything.
func (u *chatUI) startToolCall(key, title, body string, countLines bool, plain string, at time.Time) {
	if writer, ok := u.out.(chatToolWriter); ok {
		writer.StartChatTool(key, title, body, countLines, at)
		return
	}
	u.printRoleAt("tool", plain, at)
}

func (u *chatUI) toolCallOutput(key, text string) {
	if writer, ok := u.out.(chatToolWriter); ok {
		writer.WriteChatToolOutput(key, chatText(text))
		return
	}
	u.printRole("tool", text)
}

func (u *chatUI) finishToolCall(key, status string) {
	if writer, ok := u.out.(chatToolWriter); ok && status != "" {
		writer.FinishChatTool(key, status)
	}
}

// chatCommandTitle puts a command's first line on its row; the rest of a
// multi-line command shows when the row is opened.
func chatCommandTitle(command string) (title, rest string) {
	first, rest, _ := strings.Cut(strings.TrimSpace(command), "\n")
	if rest = strings.TrimRight(rest, "\n"); rest != "" {
		rest += "\n"
	}
	return "$ " + first, rest
}

// chatCommandStatus names how a finished command went wrong, or "" when it
// went fine.
func chatCommandStatus(raw json.RawMessage) string {
	var item struct {
		Status   string `json:"status"`
		ExitCode *int   `json:"exitCode"`
	}
	_ = json.Unmarshal(raw, &item)
	if item.ExitCode != nil && *item.ExitCode != 0 {
		return "exit " + strconv.Itoa(*item.ExitCode)
	}
	return chatToolFailure(item.Status)
}

func chatToolFailure(status string) string {
	switch status = strings.TrimSpace(chatText(status)); strings.ToLower(status) {
	case "", "completed", "applied", "inprogress", "in_progress":
		return ""
	}
	return status
}

func (o *chatTerminalOutput) StartChatTool(key, title, body string, countLines bool, at time.Time) {
	o.startFold("tool", key, title, body, countLines, at)
}

// StartChatFold adds text to a message of role, folded behind a row with
// title that a click opens, such as the rest of a long command.
func (o *chatTerminalOutput) StartChatFold(role, key, title, body string) {
	o.startFold(role, key, title, body, false, time.Now())
}

func (o *chatTerminalOutput) startFold(role, key, title, body string, countLines bool, at time.Time) {
	if key == "" {
		o.toolSeq++
		key = "#" + strconv.Itoa(o.toolSeq)
	}
	title = strings.Join(strings.Fields(chatText(title)), " ")
	entry := &chatTerminalEntry{role: role, agent: o.agent, at: at, tool: &chatToolCall{key: key, title: title, countLines: countLines}}
	o.entries = append(o.entries, entry)
	o.viewDirty = true
	o.appendTo(entry, chatText(body))
}

// WriteChatToolOutput adds output to the tool call with key, or to the
// transcript when that call is not shown.
func (o *chatTerminalOutput) WriteChatToolOutput(key, text string) {
	if entry := o.toolEntry(key); entry != nil {
		o.appendTo(entry, text)
		return
	}
	o.append("tool", text)
}

func (o *chatTerminalOutput) FinishChatTool(key, status string) {
	if call := o.toolByKey(key); call != nil {
		call.status = strings.TrimSpace(chatText(status))
		o.viewDirty = true
	}
}

// toggleTool opens or closes the rows with key and reports whether any is shown.
func (o *chatTerminalOutput) toggleTool(key string) bool {
	if o.toolEntry(key) == nil {
		return false
	}
	if o.expanded == nil {
		o.expanded = map[string]bool{}
	}
	if o.expanded[key] {
		delete(o.expanded, key)
	} else {
		o.expanded[key] = true
	}
	o.viewDirty = true
	return true
}

// toolEntry finds the newest tool call with key.
func (o *chatTerminalOutput) toolEntry(key string) *chatTerminalEntry {
	if key == "" {
		return nil
	}
	for i := len(o.entries) - 1; i >= 0; i-- {
		if entry := o.entries[i]; entry.tool != nil && entry.tool.key == key {
			return entry
		}
	}
	return nil
}

func (o *chatTerminalOutput) toolByKey(key string) *chatToolCall {
	if entry := o.toolEntry(key); entry != nil {
		return entry.tool
	}
	return nil
}

// toolLines renders a tool call: its row, then its output when opened.
func (o *chatTerminalOutput) toolLines(entry *chatTerminalEntry, width int) []chatViewLine {
	call := entry.tool
	lineCount := strings.Count(entry.text.String(), "\n")
	if entry.hasLine && entry.pending.Len() > 0 {
		lineCount++
	}
	open := o.expanded[call.key]
	marker, suffix := "  ", ""
	if lineCount > 0 {
		marker = "▸ "
		if open {
			marker = "▾ "
		}
	}
	if call.countLines && lineCount > 0 {
		suffix += fmt.Sprintf(" · %d line", lineCount)
		if lineCount > 1 {
			suffix += "s"
		}
	}
	if call.status != "" {
		suffix += " · " + call.status
	}
	title := fitChatView(call.title, max(1, width-4-uniseg.StringWidth(suffix)))
	row := chatViewLine{text: "  " + marker + title + suffix, kind: "tool-call", role: "tool"}
	if lineCount == 0 {
		return []chatViewLine{row}
	}
	row.action = "tool:" + call.key
	if !open {
		return []chatViewLine{row}
	}
	return append([]chatViewLine{row}, entry.bodyLines(width)...)
}

// expandLine renders one line of an entry; a tool call's output is indented
// under its row.
func (e *chatTerminalEntry) expandLine(raw string, width int) ([]chatViewLine, string) {
	if e.tool == nil {
		return expandChatTranscriptLine(raw, width, e.role, e.inCode)
	}
	lines, inCode := expandChatTranscriptLine(raw, max(1, width-2), e.role, e.inCode)
	for i := range lines {
		lines[i].text = "  " + lines[i].text
		if len(lines[i].spans) > 0 {
			lines[i].codeStart += 2
		}
	}
	return lines, inCode
}
