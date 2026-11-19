package jara

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rivo/uniseg"
	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

type menuItem struct {
	Title, Detail, Workspace string
	Group                    string
	Path                     string
	Agent                    string
	Kind                     string
	Action                   string
	Indent                   int
}

func menuPageSize(width, height int) int {
	if height < 14 {
		return max(1, min(9, (height-5)/2))
	}
	return max(1, min(9, (height-7)/3))
}

func menuLines(title string, items []menuItem, selected, width, height int) []string {
	return menuLinesFiltered(title, items, selected, width, height, "", len(items))
}

// The layout stays fixed when selection changes; only its highlight moves.
func menuLinesFiltered(title string, items []menuItem, selected, width, height int, filter string, total int) []string {
	width = max(1, width-1)
	if height < 7 || width < 15 {
		return []string{textutil.FitText("Enlarge terminal · q exits", width)}
	}
	if len(items) == 0 {
		return emptyMenuLines(title, width, height, filter, total)
	}
	selected = max(0, min(selected, len(items)-1))
	size := menuPageSize(width+1, height)
	start := selected / size * size
	end := min(start+size, len(items))
	panel := min(width, 100)
	gutter := strings.Repeat(" ", max(0, (width-panel)/2))
	count := menuCount(start, end, len(items), total, filter)
	lines := []string{textutil.FitText(" JARA  /  "+title, panel), textutil.FitText(" "+count+" · n/p page", panel), textutil.FitText(menuSearchLabel(filter), panel)}
	spacious := height >= 14
	if spacious {
		lines = append(lines, strings.Repeat("─", panel))
	}
	for i := start; i < end; i++ {
		mark := "  "
		if i == selected {
			mark = "▸ "
		}
		icon := menuKindIcon(items[i].Kind)
		lines = append(lines, textutil.FitText(mark+fmt.Sprint(i-start+1)+" "+icon+"  "+items[i].Title, panel), textutil.FitText("       "+items[i].Detail, panel))
		if spacious {
			lines = append(lines, "")
		}
	}
	if spacious {
		for len(lines) < height-3 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Repeat("─", panel))
	}
	if filter != "" {
		lines = append(lines, textutil.FitText(" Backspace edit · → open", panel), textutil.FitText(" ← back · Ctrl-C quit", panel))
	} else {
		lines = append(lines, textutil.FitText(" ↑↓ move · →/Enter open", panel), textutil.FitText(" ←/⌫ back · Ctrl-C quit", panel))
	}
	for i := range lines {
		lines[i] = gutter + lines[i]
	}
	return lines
}

func menuCount(start, end, count, total int, filter string) string {
	if count == 0 {
		return "No items"
	}
	if filter != "" {
		return fmt.Sprintf("%d–%d of %d matches · %d total", start+1, end, count, total)
	}
	return fmt.Sprintf("%d–%d of %d", start+1, end, count)
}

func menuSearchLabel(filter string) string {
	if filter == "" {
		return " /  Search…"
	}
	return " /  " + filter + "▏"
}

func menuKindIcon(kind string) string {
	switch kind {
	case "conversation":
		return "○"
	case "terminals":
		return "▤"
	case "running":
		return "●"
	case "terminal", "background":
		return "●"
	case "project", "workspace":
		return "◇"
	case "new":
		return "+"
	case "tools":
		return "✦"
	case "group":
		return "…"
	case "history":
		return "↻"
	default:
		return "›"
	}
}

func menuMatches(items []menuItem, filter string) []int {
	indices := make([]int, 0, len(items))
	needle := strings.ToLower(textutil.SanitizeText(filter))
	for i, item := range items {
		searchable := strings.ToLower(textutil.SanitizeText(strings.Join([]string{item.Title, item.Detail, item.Workspace, item.Group, item.Kind}, " ")))
		if needle == "" || strings.Contains(searchable, needle) {
			indices = append(indices, i)
		}
	}
	return indices
}

func emptyMenuLines(title string, width, height int, filter string, total int) []string {
	panel := min(width, 100)
	gutter := strings.Repeat(" ", max(0, (width-panel)/2))
	lines := []string{" JARA  /  " + title, fmt.Sprintf(" 0 matches · %d total", total), menuSearchLabel(filter), " No conversations or items found", " Try a shorter search", " Backspace edit · Ctrl-U clear", " ← back · Ctrl-C quit"}
	for i := range lines {
		lines[i] = gutter + textutil.FitText(lines[i], panel)
	}
	return lines
}
func selectMenu(title string, items []menuItem) (int, error) { return selectMenuAt(title, items, 0) }
func selectMenuAt(title string, items []menuItem, initial int) (int, error) {
	if len(items) == 0 {
		return 0, io.EOF
	}
	if n, handled, err := terminalMenuAt(title, items, initial); handled {
		return n, err
	}
	fmt.Println(textutil.SanitizeText(title))
	for i, item := range items {
		fmt.Printf("  %d) %s%s — %s\n", i+1, strings.Repeat("  ", item.Indent), textutil.SanitizeText(item.Title), textutil.SanitizeText(item.Detail))
	}
	fmt.Print("Choose a number (q to exit): ")
	reader := bufio.NewReader(os.Stdin)
	for {
		s, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		s = strings.TrimSpace(s)
		if s == "q" {
			return 0, io.EOF
		}
		n, err := strconv.Atoi(s)
		if err == nil && n >= 1 && n <= len(items) {
			return n - 1, nil
		}
		fmt.Print("Invalid choice; enter a number or q: ")
	}
}
func workspaceTitle(w Workspace) string {
	name := strings.TrimSpace(w.Name)
	nameIsID := name == w.ID || isGeneratedWorkspaceID(name)
	if origin, project, _, _, ok := workspaceOriginParts(w.URL); ok {
		if name != "" && !nameIsID && !strings.EqualFold(name, project) {
			return textutil.SanitizeText(origin + " · " + name)
		}
		return textutil.SanitizeText(origin)
	}
	if name != "" && !nameIsID {
		return textutil.SanitizeText(name)
	}
	if w.URL != "" {
		name := strings.SplitN(strings.SplitN(w.URL, "?", 2)[0], "#", 2)[0]
		name = name[strings.LastIndexAny(name, "/:")+1:]
		name = strings.TrimSuffix(name, ".git")
		if name != "" {
			return textutil.SanitizeText(name)
		}
	}
	return "Copied workspace"
}

func workspaceTerminalTitle(w Workspace) string {
	_, project, host, _, ok := workspaceOriginParts(w.URL)
	if !ok {
		return textutil.FitText(workspaceTitle(w), 24)
	}
	provider := workspaceProviderLabel(host)
	suffix := " · " + provider
	projectWidth := 24 - uniseg.StringWidth(suffix)
	return textutil.FitText(project, projectWidth) + suffix
}

func workspaceProviderLabel(host string) string {
	host = strings.ToLower(host)
	switch {
	case strings.Contains(host, "github"):
		return "GitHub"
	case strings.Contains(host, "gitlab"):
		return "GitLab"
	case strings.Contains(host, "gitea"):
		return "Gitea"
	case strings.Contains(host, "codeberg"):
		return "Codeberg"
	}
	host = strings.SplitN(host, ":", 2)[0]
	parts := strings.Split(host, ".")
	provider := parts[0]
	if provider == "git" && len(parts) > 1 {
		provider = parts[1]
	}
	return textutil.FitText(provider, 10)
}

func workspaceOriginParts(origin string) (display, project, host, namespace string, ok bool) {
	if isSSHGitOrigin(origin) {
		if target, err := parseGitCredentialTarget(origin); err == nil {
			origin = target.URL
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil && u.Scheme != "ssh" {
		return "", "", "", "", false
	}
	host = strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return "", "", "", "", false
	}
	project = strings.TrimSuffix(parts[len(parts)-1], ".git")
	if project == "" {
		return "", "", "", "", false
	}
	parts[len(parts)-1] = project
	if len(parts) > 1 {
		namespace = parts[len(parts)-2]
	}
	return strings.Join(parts, "/") + "@" + host, project, host, namespace, true
}

func isGeneratedWorkspaceID(value string) bool {
	if !strings.HasPrefix(value, "w-") {
		return false
	}
	id := strings.TrimPrefix(value, "w-")
	if len(id) != 24 {
		return false
	}
	for _, char := range id {
		if !('0' <= char && char <= '9') && !('a' <= char && char <= 'f') {
			return false
		}
	}
	return true
}

func sessionItem(w Workspace, s Session, number int) menuItem {
	title := strings.TrimSpace(s.Label)
	if title == "" {
		if s.ChatDaemon {
			title = "Persistent Jara session"
		} else if s.Background {
			title = fmt.Sprintf("Background run #%d", number)
		} else {
			title = fmt.Sprintf("Jara terminal #%d", number)
		}
	}
	state := "Running"
	if s.Background {
		state = fmt.Sprintf("Background #%d", number)
	}
	if s.Dead {
		state = "Exited"
		if s.Background {
			state = fmt.Sprintf("Finished background #%d", number)
		}
	}
	viewers := fmt.Sprintf("%d viewers", s.Attached)
	if s.Attached == 1 {
		viewers = "1 viewer"
	}
	if s.Attached == 0 {
		viewers = "detached"
	}
	detail := []string{state, viewers}
	if s.Agent != "" {
		detail = append([]string{protocol.AgentDisplayName(s.Agent)}, detail...)
	}
	if validAgentState(s.AgentState) {
		detail = append(detail, "Agent "+s.AgentState)
	}
	if started := sessionStartedLabel(s); started != "" {
		detail = append(detail, started)
	}
	kind := "terminal"
	if s.Background {
		kind = "background"
	}
	if s.ChatDaemon {
		kind = "chat-daemon"
	}
	return menuItem{Title: title, Detail: strings.Join(detail, " · "), Workspace: w.ID, Group: workspaceTitle(w), Kind: kind, Action: s.Name}
}

func sessionStartedLabel(session Session) string {
	created := session.CreatedAt
	if created == 0 {
		created = session.LastAttach
	}
	if created == 0 {
		return ""
	}
	age := time.Since(time.Unix(0, created))
	switch {
	case age < time.Minute:
		return "just started"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours()/24))
	}
}
