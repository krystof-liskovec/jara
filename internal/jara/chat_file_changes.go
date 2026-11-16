package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

const (
	chatFileChangeDiffLimit      = 80
	chatFileChangeTotalDiffLimit = 240
)

// startFileChangeCall shows the agent's edits as one row that opens to the
// diffs; lead goes before the full text on outputs that show everything.
func (u *chatUI) startFileChangeCall(item protocol.HistoryItem, lead string, at time.Time) {
	u.startToolCall(item.ID, chatFileChangeTitle(item.Status, item.Changes), chatFileChangeRowDetails(item.Changes), false,
		lead+formatChatFileChangeList(item.Status, item.Changes), at)
}

// chatFileChangeTitle sums up edits in one line, such as
// "Edited chat_view.go · +3 −1" or "Edited 3 files · +10 −4".
func chatFileChangeTitle(status string, changes []protocol.FileChange) string {
	title := fmt.Sprintf("Edited %d files", len(changes))
	switch len(changes) {
	case 0:
		title = "File changes contained no file paths"
	case 1:
		verb := "Edited"
		switch chatFileChangeKind(changes[0].Kind) {
		case "added":
			verb = "Created"
		case "deleted":
			verb = "Deleted"
		}
		title = verb + " " + chatFileChangePath(changes[0])
	}
	added, removed := 0, 0
	for _, change := range changes {
		a, r := chatFileChangeCounts(change.Diff)
		added, removed = added+a, removed+r
	}
	if added+removed > 0 {
		title += fmt.Sprintf(" · +%d −%d", added, removed)
	}
	if failure := chatToolFailure(status); failure != "" {
		title += " · " + failure
	}
	return title
}

func formatChatFileChangeList(status string, changes []protocol.FileChange) string {
	if len(changes) == 0 {
		return "File changes contained no file paths.\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "File changes (%d files)", len(changes))
	status = strings.TrimSpace(chatText(status))
	if status != "" && !strings.EqualFold(status, "completed") && !strings.EqualFold(status, "applied") {
		b.WriteString(" · " + status)
	}
	b.WriteString(":\n")
	b.WriteString(chatFileChangeDetails(changes))
	return b.String()
}

// chatFileChangeRowDetails is what a file-change row opens to: the diff
// alone when its row already names the one file.
func chatFileChangeRowDetails(changes []protocol.FileChange) string {
	details := chatFileChangeDetails(changes)
	if len(changes) == 1 {
		_, details, _ = strings.Cut(details, "\n")
	}
	return details
}

// chatFileChangeDetails lists every changed file, then the diffs, so a
// narrow screen shows what changed before scrolling through the code.
func chatFileChangeDetails(changes []protocol.FileChange) string {
	var b strings.Builder
	paths := make([]string, len(changes))
	for index, change := range changes {
		path := chatFileChangePath(change)
		paths[index] = path
		b.WriteString("- " + path)
		if kind := chatFileChangeKind(change.Kind); kind != "" {
			b.WriteString(" · " + kind)
		}
		if added, removed := chatFileChangeCounts(change.Diff); added+removed > 0 {
			fmt.Fprintf(&b, " · +%d −%d", added, removed)
		}
		b.WriteByte('\n')
	}
	remainingDiffLines := chatFileChangeTotalDiffLimit
	for index, change := range changes {
		if change.Diff == "" {
			continue
		}
		path := paths[index]
		if len(changes) > 1 {
			b.WriteString("  " + path + "\n")
		}
		if remainingDiffLines <= 0 {
			b.WriteString("  … diff omitted (display limit reached)\n")
			continue
		}
		diffLines := strings.Split(strings.TrimRight(change.Diff, "\r\n"), "\n")
		shown := min(len(diffLines), min(chatFileChangeDiffLimit, remainingDiffLines))
		b.WriteString("  ```diff " + strings.Join(strings.Fields(path), "_") + "\n")
		for _, line := range diffLines[:shown] {
			b.WriteString(line + "\n")
		}
		if omitted := len(diffLines) - shown; omitted > 0 {
			b.WriteString(fmt.Sprintf("… %d diff lines omitted\n", omitted))
		}
		b.WriteString("  ```\n")
		remainingDiffLines -= shown
	}
	return b.String()
}

func chatFileChangePath(change protocol.FileChange) string {
	path := strings.Join(strings.Fields(chatText(textutil.FirstNonEmpty(change.Path, change.NewPath, change.OldPath))), " ")
	if path == "" {
		return "Unknown file"
	}
	return path
}

func chatFileChangeKind(raw json.RawMessage) string {
	return chatFileChangeKindValue(raw, 0)
}

func chatFileChangeKindValue(raw json.RawMessage, depth int) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if label, _ := chatFileChangeKindLabel(text); label != "" {
			return label
		}
		return ""
	}
	if depth >= 4 {
		return "changed"
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return "changed"
	}
	for _, key := range []string{"type", "kind", "action", "changeType", "change_type", "name"} {
		if field, ok := fields[key]; ok {
			if label := chatFileChangeKindValue(field, depth+1); label != "" {
				return label
			}
		}
	}
	for key := range fields {
		if label, ok := chatFileChangeKindLabel(key); ok {
			return label
		}
	}
	return "changed"
}

func chatFileChangeKindLabel(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "add", "added", "create", "created":
		return "added", true
	case "delete", "deleted", "remove", "removed":
		return "deleted", true
	case "update", "updated", "modify", "modified":
		return "updated", true
	default:
		return strings.TrimSpace(chatText(kind)), false
	}
}

func chatFileChangeCounts(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return added, removed
}
