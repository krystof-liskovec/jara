package jara

import (
	"encoding/json"
	"fmt"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
)

const (
	chatFileChangeDiffLimit      = 80
	chatFileChangeTotalDiffLimit = 240
)

func formatChatFileChanges(raw json.RawMessage) string {
	var item struct {
		Status  string                `json:"status"`
		Changes []protocol.FileChange `json:"changes"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return "File change details could not be read.\n"
	}
	return formatChatFileChangeList(item.Status, item.Changes)
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

	remainingDiffLines := chatFileChangeTotalDiffLimit
	for _, change := range changes {
		path := change.Path
		if path == "" {
			path = change.NewPath
		}
		if path == "" {
			path = change.OldPath
		}
		path = strings.Join(strings.Fields(chatText(path)), " ")
		if path == "" {
			path = "Unknown file"
		}
		b.WriteString("- " + path)
		if kind := chatFileChangeKind(change.Kind); kind != "" {
			b.WriteString(" · " + kind)
		}
		added, removed := chatFileChangeCounts(change.Diff)
		if added+removed > 0 {
			fmt.Fprintf(&b, " · +%d −%d", added, removed)
		}
		b.WriteByte('\n')

		diffLines := strings.Split(strings.TrimRight(change.Diff, "\r\n"), "\n")
		if change.Diff == "" {
			continue
		}
		if remainingDiffLines <= 0 {
			b.WriteString("  … diff omitted (display limit reached)\n")
			continue
		}
		shown := min(len(diffLines), min(chatFileChangeDiffLimit, remainingDiffLines))
		b.WriteString("  ```diff " + strings.Join(strings.Fields(path), "_") + "\n")
		for _, line := range diffLines[:shown] {
			b.WriteString(line + "\n")
		}
		omitted := len(diffLines) - shown
		if omitted > 0 {
			b.WriteString(fmt.Sprintf("… %d diff lines omitted\n", omitted))
		}
		b.WriteString("  ```\n")
		remainingDiffLines -= shown
	}
	return b.String()
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
