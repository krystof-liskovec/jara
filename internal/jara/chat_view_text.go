package jara

import (
	"strings"
	"time"

	"github.com/rivo/uniseg"
)

func sanitizeChatViewText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	parts := strings.Split(s, "\n")
	for i := range parts {
		parts[i] = menuText(parts[i])
	}
	return strings.Join(parts, "\n")
}

func expandChatTranscript(messages []chatViewMessage, width int) []chatViewLine {
	var lines []chatViewLine
	previousRole := ""
	for _, message := range messages {
		role := message.Role
		label := "• Jara"
		kind := "role-system"
		switch role {
		case "user":
			label, kind = "● You", "role-user"
		case "assistant":
			label, kind = "✦ Codex", "role-assistant"
		case "tool":
			label, kind = "⌁ Tool", "role-tool"
		case "progress":
			label, kind = "↳ Codex progress", "role-progress"
		case "subagent":
			label, kind = "↳ Subagent activity", "role-subagent"
		}
		if chatViewNeedsMessageGap(previousRole, role) {
			lines = append(lines, chatViewLine{kind: "message-gap"})
		}
		title := strings.TrimSpace(menuText(message.Title))
		if title != "" {
			label += " · " + title
		}
		if !message.At.IsZero() {
			label += " · " + chatMessageTimestamp(message.At)
		}
		lines = append(lines, chatViewLine{text: label, kind: kind, role: role})
		lines = append(lines, expandChatMessage(message.Text, width, role)...)
		previousRole = role
	}
	return lines
}

func chatViewActivityRole(role string) bool {
	return role == "progress" || role == "subagent"
}

func chatViewNeedsMessageGap(previousRole, role string) bool {
	if previousRole == "" {
		return false
	}
	if previousRole == "user" && chatViewActivityRole(role) {
		return true
	}
	return !chatViewActivityRole(previousRole) && !chatViewActivityRole(role)
}

func chatMessageTimestamp(at time.Time) string {
	local := at.Local()
	now := time.Now().In(local.Location())
	if local.Year() == now.Year() && local.YearDay() == now.YearDay() {
		return local.Format("15:04")
	}
	return local.Format("Jan 2 · 15:04")
}

func expandChatMessage(text string, width int, role string) []chatViewLine {
	text = sanitizeChatViewText(text)
	if text == "" {
		return nil
	}
	width = max(1, width)
	var lines []chatViewLine
	inCode := false
	for _, raw := range strings.Split(text, "\n") {
		row, nextCode := expandChatTranscriptLine(raw, width, role, inCode)
		lines = append(lines, row...)
		inCode = nextCode
	}
	return lines
}

func expandChatTranscriptLine(raw string, width int, role string, inCode bool) ([]chatViewLine, bool) {
	raw = menuText(strings.ReplaceAll(raw, "\t", "    "))
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "```") {
		return nil, !inCode
	}
	if inCode {
		return wrapChatLine(raw, width, "  │ ", "  │ ", "code", role, true), inCode
	}
	if trimmed == "" {
		return []chatViewLine{{kind: "body", role: role}}, inCode
	}
	if prefix, body, ok := chatMarkdownHeading(raw); ok {
		return wrapChatLine(body, width, prefix, "   ", "heading", role, false), inCode
	}
	if prefix, body, ok := chatMarkdownBullet(raw); ok {
		return wrapChatLine(body, width, prefix, "    ", "bullet", role, false), inCode
	}
	return wrapChatLine(raw, width, "  ", "  ", "body", role, false), inCode
}

func chatMarkdownHeading(line string) (prefix, body string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	count := 0
	for count < len(trimmed) && count < 6 && trimmed[count] == '#' {
		count++
	}
	if count == 0 || count == len(trimmed) || trimmed[count] != ' ' {
		return "", "", false
	}
	return "  ", strings.TrimSpace(trimmed[count:]), true
}

func chatMarkdownBullet(line string) (prefix, body string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(trimmed) >= 2 && (trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '+') && trimmed[1] == ' ' {
		return "  • ", strings.TrimSpace(trimmed[2:]), true
	}
	for i := 0; i < len(trimmed) && i < 4 && trimmed[i] >= '0' && trimmed[i] <= '9'; i++ {
		if i+1 < len(trimmed) && trimmed[i+1] == '.' && i+2 < len(trimmed) && trimmed[i+2] == ' ' {
			return "  • ", strings.TrimSpace(trimmed[i+3:]), true
		}
	}
	return "", "", false
}

func wrapChatLine(text string, width int, prefix, continuation, kind, role string, hard bool) []chatViewLine {
	available := max(1, width-uniseg.StringWidth(prefix))
	continuationWidth := max(1, width-uniseg.StringWidth(continuation))
	var chunks []string
	if hard {
		chunks = wrapChatHard(text, available)
	} else {
		chunks = wrapChatWords(text, available, continuationWidth)
	}
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	lines := make([]chatViewLine, 0, len(chunks))
	for i, chunk := range chunks {
		linePrefix := prefix
		if i > 0 {
			linePrefix = continuation
		}
		lines = append(lines, chatViewLine{text: linePrefix + chunk, kind: kind, role: role})
	}
	return lines
}

func wrapChatWords(text string, firstWidth, nextWidth int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	line, width := "", firstWidth
	for _, word := range words {
		wordWidth := uniseg.StringWidth(word)
		if line != "" && uniseg.StringWidth(line)+1+wordWidth <= width {
			line += " " + word
			continue
		}
		if line != "" {
			lines = append(lines, line)
			line, width = "", nextWidth
		}
		if wordWidth <= width {
			line = word
			continue
		}
		parts := wrapChatHard(word, width)
		if len(parts) > 1 {
			lines = append(lines, parts[:len(parts)-1]...)
		}
		line = parts[len(parts)-1]
	}
	if line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}

func wrapChatHard(text string, width int) []string {
	width = max(1, width)
	if text == "" {
		return []string{""}
	}
	var lines []string
	var line strings.Builder
	used := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		part, cells := g.Str(), g.Width()
		if used+cells > width && used > 0 {
			lines = append(lines, line.String())
			line.Reset()
			used = 0
		}
		line.WriteString(part)
		used += cells
	}
	lines = append(lines, line.String())
	return lines
}

func fitChatView(text string, width int) string {
	width = max(0, width)
	text = sanitizeChatViewText(text)
	text = strings.ReplaceAll(text, "\n", " ")
	if uniseg.StringWidth(text) <= width {
		return text
	}
	if width == 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		cells := g.Width()
		if used+cells > width-1 {
			break
		}
		b.WriteString(g.Str())
		used += cells
	}
	return b.String() + "…"
}

func wrapChatComposer(text string, cursorLine, cursorColumn, width int) ([]string, int, int) {
	width = max(1, width)
	lines := strings.Split(sanitizeChatViewText(text), "\n")
	cursorLine = max(0, min(cursorLine, len(lines)-1))
	cursorColumn = max(0, cursorColumn)
	var visual []string
	cursorVisualLine, cursorVisualColumn := 0, 0
	for lineIndex, line := range lines {
		chunks, starts := wrapChatHardWithStarts(line, width)
		if lineIndex == cursorLine {
			segment := 0
			for i := range starts {
				if cursorColumn < starts[i] {
					break
				}
				segment = i
			}
			cursorVisualLine = len(visual) + segment
			cursorVisualColumn = max(0, min(width, cursorColumn-starts[segment]))
			if cursorColumn > 0 && cursorColumn == starts[segment]+width && segment+1 < len(chunks) {
				cursorVisualLine++
				cursorVisualColumn = 0
			}
		}
		visual = append(visual, chunks...)
	}
	return visual, cursorVisualLine, cursorVisualColumn
}

func wrapChatHardWithStarts(text string, width int) ([]string, []int) {
	if text == "" {
		return []string{""}, []int{0}
	}
	var lines []string
	var starts []int
	var line strings.Builder
	used, offset := 0, 0
	starts = append(starts, 0)
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		part, cells := g.Str(), g.Width()
		if used+cells > width && used > 0 {
			lines = append(lines, line.String())
			line.Reset()
			used = 0
			starts = append(starts, offset)
		}
		line.WriteString(part)
		used += cells
		offset += cells
	}
	lines = append(lines, line.String())
	return lines, starts
}
