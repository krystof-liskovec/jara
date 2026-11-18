package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// formatChatApproval shows what an approval would allow: the command as
// written, in a code block so nothing in it is read as markdown, or the file
// changes with their diffs; the working folder and reason; and every other
// detail of the request and its item by name, since any of them can widen
// what is allowed. Only bookkeeping IDs and repeated fields are left out.
func formatChatApproval(m appMessage, item json.RawMessage) string {
	view := chatApprovalParts(m, item)
	return view.heading + chatApprovalCode(view.command) + view.rest
}

// chatApprovalView is an approval in parts, so the terminal can fold a long
// command: the heading, the command as written ("" for file changes), and
// everything after it.
type chatApprovalView struct{ heading, command, rest string }

// chatApprovalCode puts command lines in a code block, which keeps them
// as written: line breaks, backticks and all.
func chatApprovalCode(command string) string {
	if command == "" {
		return ""
	}
	return "```bash\n" + command + "\n```\n"
}

// Commands longer than chatApprovalFoldAfter lines show their first
// chatApprovalPreviewLines; a row opens the rest.
const (
	chatApprovalPreviewLines = 8
	chatApprovalFoldAfter    = 10
)

// chatApprovalPreview splits a long command into the lines shown and the
// lines a click opens; a short one is shown whole.
func chatApprovalPreview(command string) (shown, hidden string, hiddenLines int) {
	lines := strings.Split(command, "\n")
	if len(lines) <= chatApprovalFoldAfter {
		return command, "", 0
	}
	return strings.Join(lines[:chatApprovalPreviewLines], "\n"), strings.Join(lines[chatApprovalPreviewLines:], "\n"), len(lines) - chatApprovalPreviewLines
}

func chatApprovalParts(m appMessage, item json.RawMessage) chatApprovalView {
	request := chatApprovalFields(m.Params)
	details := chatApprovalFields(item)
	var view chatApprovalView
	var b strings.Builder
	command := textutil.FirstNonEmpty(chatApprovalString(request, "command"), chatApprovalString(details, "command"))
	if m.Method == "item/commandExecution/requestApproval" || command != "" {
		view.heading = "**Run this command?**\n"
		view.command = sanitizeChatViewText(command) // keeps its lines
	} else {
		view.heading = "**Apply these file changes?**\n"
	}
	if cwd := textutil.FirstNonEmpty(chatApprovalString(request, "cwd"), chatApprovalString(details, "cwd")); cwd != "" {
		b.WriteString("In " + textutil.SanitizeText(cwd) + "\n")
	}
	if reason := chatApprovalString(request, "reason"); reason != "" {
		b.WriteString("Reason: " + textutil.SanitizeText(reason) + "\n")
	}
	if raw, ok := details["changes"]; ok {
		var changes []protocol.FileChange
		if json.Unmarshal(raw, &changes) == nil && len(changes) > 0 {
			b.WriteString(chatFileChangeDetails(changes))
		}
	} else if itemID := chatApprovalString(request, "itemId"); itemID != "" && len(item) == 0 && command == "" {
		b.WriteString("The change itself was not received (item " + textutil.SanitizeText(itemID) + ").\n")
	}
	for _, line := range append(chatApprovalOthers(request), chatApprovalOthers(details)...) {
		b.WriteString(line + "\n")
	}
	view.rest = b.String()
	return view
}

// chatFoldWriter is the transcript that can fold text into a row a click
// opens.
type chatFoldWriter interface {
	StartChatFold(role, key, title, body string)
}

// printApproval shows an approval request. In the terminal a long command
// shows its first lines and a row that opens the rest; elsewhere it shows
// whole.
func (u *chatUI) printApproval(m appMessage, item json.RawMessage, footer string) {
	view := chatApprovalParts(m, item)
	shown, hidden, count := chatApprovalPreview(view.command)
	folder, ok := u.out.(chatFoldWriter)
	if hidden == "" || !ok {
		u.print("\n" + view.heading + chatApprovalCode(view.command) + view.rest + footer)
		return
	}
	u.print("\n" + view.heading + chatApprovalCode(shown))
	folder.StartChatFold("system", "approval:"+string(m.ID), fmt.Sprintf("%d more lines of the command", count), chatApprovalCode(hidden))
	u.print(view.rest + footer)
}

// chatApprovalShown are fields shown above, or bookkeeping.
var chatApprovalShown = []string{"command", "cwd", "reason", "changes", "threadId", "turnId", "itemId", "id", "type", "status", "commandActions"}

func chatApprovalFields(raw json.RawMessage) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &fields)
	return fields
}

func chatApprovalString(fields map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(fields[key], &value)
	return strings.TrimSpace(value)
}

// chatApprovalOthers lists the remaining fields as "name: value", skipping
// empty ones.
func chatApprovalOthers(fields map[string]json.RawMessage) []string {
	var lines []string
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		raw := bytes.TrimSpace(fields[key])
		if slices.Contains(chatApprovalShown, key) || len(raw) == 0 || string(raw) == "null" || string(raw) == `""` || string(raw) == "[]" || string(raw) == "{}" {
			continue
		}
		value := string(raw)
		var text string
		if json.Unmarshal(raw, &text) == nil {
			value = text
		} else {
			var compact bytes.Buffer
			encoder := json.NewEncoder(&compact)
			encoder.SetEscapeHTML(false)
			var decoded any
			if json.Unmarshal(raw, &decoded) == nil && encoder.Encode(decoded) == nil {
				value = strings.TrimSpace(compact.String())
			}
		}
		lines = append(lines, textutil.SanitizeText(key)+": "+textutil.SanitizeText(value))
	}
	return lines
}
