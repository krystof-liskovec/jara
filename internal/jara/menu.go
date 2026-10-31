package jara

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

type menuItem struct {
	Title, Detail, Workspace string
	Kind                     string
	Action                   string
}

var terminalEscapes = regexp.MustCompile("\\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\))")

// Names originate in repositories and must not control the terminal.
func menuText(s string) string {
	s = terminalEscapes.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		if r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return -1
		}
		return r
	}, s)
}
func fitMenu(s string, cells int) string {
	if cells <= 0 {
		return ""
	}
	s = menuText(s)
	if uniseg.StringWidth(s) <= cells {
		return s
	}
	var b strings.Builder
	used := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		if used+g.Width() > cells-1 {
			break
		}
		b.WriteString(g.Str())
		used += g.Width()
	}
	return b.String() + "…"
}
func menuPageSize(width, height int) int {
	if width >= 90 {
		return max(1, height-6)
	}
	lines := 1
	if width < 60 {
		lines = 2
	}
	return max(1, min(9, (height-6)/lines))
}

// Each line is bounded in terminal cells, leaving the last column unused to
// prevent automatic wrapping. Short screens retain navigation and pagination.
func menuLines(title string, items []menuItem, selected, width, height int) []string {
	return menuLinesFiltered(title, items, selected, width, height, "", len(items))
}

func menuLinesFiltered(title string, items []menuItem, selected, width, height int, filter string, total int) []string {
	width = max(1, width-1)
	if height < 7 || width+1 < 16 {
		return []string{fitMenu("Enlarge terminal · q exits", width)}
	}
	if len(items) == 0 {
		return emptyMenuLines(title, width, height, filter, total)
	}
	selected = max(0, min(selected, len(items)-1))
	if width >= 90 && height >= 14 {
		return menuPanelLines(title, items, selected, width, height, filter, total)
	}
	size := menuPageSize(width+1, height)
	start := selected / size * size
	end := min(start+size, len(items))
	lines := []string{fitMenu("JARA · "+title, width), fitMenu(menuCount(start, end, len(items), total, filter), width), fitMenu(menuSearchLabel(filter), width)}
	for i := start; i < end; i++ {
		mark := "  "
		if i == selected {
			mark = "> "
		}
		prefix := mark + menuKindIcon(items[i].Kind) + " "
		if width+1 < 60 {
			lines = append(lines, fitMenu(prefix+items[i].Title, width), fitMenu("    "+items[i].Detail, width))
		} else {
			titleCells := min(38, width/2)
			name := fitMenu(items[i].Title, titleCells)
			padding := strings.Repeat(" ", max(1, titleCells-uniseg.StringWidth(name)+2))
			lines = append(lines, fitMenu(prefix+name+padding+items[i].Detail, width))
		}
	}
	identity := ""
	if items[selected].Workspace != "" {
		identity = fitMenu("ID: "+items[selected].Workspace, width)
	} else if items[selected].Kind != "" {
		identity = fitMenu("Selected: "+menuKindLabel(items[selected].Kind), width)
	}
	openHelp, moveHelp := "1-9 open · / search · j/k move · n/p page", "Enter open · q back · Esc quit"
	if filter != "" {
		openHelp, moveHelp = "Backspace edit · Ctrl-U clear", "Enter open · Esc clear · Ctrl-C back"
	}
	if width < 55 && filter == "" {
		openHelp, moveHelp = "1-9 open · / search · n/p page", "j/k move · Enter open · q back"
	}
	if width < 29 && filter == "" {
		openHelp, moveHelp = "1-9 open · / search", "Enter open · q back"
	}
	if height >= 9 {
		lines = append(lines, identity)
	}
	lines = append(lines, fitMenu(openHelp, width), fitMenu(moveHelp, width))
	return lines
}

func menuPanelLines(title string, items []menuItem, selected, width, height int, filter string, total int) []string {
	pageSize := menuPageSize(width+1, height)
	start := selected / pageSize * pageSize
	end := min(start+pageSize, len(items))
	leftWidth := (width - 3) * 52 / 100
	rightWidth := width - leftWidth - 3
	lines := []string{
		fitMenu("JARA · "+title, width),
		fitMenu(menuCount(start, end, len(items), total, filter), width),
		fitMenu(menuSearchLabel(filter), width),
		strings.Repeat("─", leftWidth) + "┬" + strings.Repeat("─", rightWidth),
	}
	preview := []string{"PREVIEW", fitMenu(items[selected].Title, rightWidth), menuKindLabel(items[selected].Kind), fitMenu(items[selected].Detail, rightWidth)}
	if items[selected].Workspace != "" {
		preview = append(preview, fitMenu("ID: "+items[selected].Workspace, rightWidth))
	}
	preview = append(preview, "", "Enter opens this item", "/ filters the list")
	rowCount := height - 6
	for row := 0; row < rowCount; row++ {
		index := start + row
		left := ""
		if index < end {
			mark := "  "
			if index == selected {
				mark = "> "
			}
			left = mark + menuKindIcon(items[index].Kind) + " " + items[index].Title
		}
		right := ""
		if row < len(preview) {
			right = preview[row]
		}
		lines = append(lines, fitMenu(left, leftWidth)+" │ "+fitMenu(right, rightWidth))
	}
	lines = append(lines, strings.Repeat("─", leftWidth)+"┴"+strings.Repeat("─", rightWidth))
	help := "↑/↓ or j/k move · n/p page · Enter open · / search · q back · Esc quit"
	if filter != "" {
		help = "Backspace edit · Ctrl-U clear · Enter open · Ctrl-C back"
	}
	lines = append(lines, fitMenu(help, width))
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
		return "Search  / type to filter"
	}
	return "Search  /" + filter + "_"
}

func menuKindIcon(kind string) string {
	switch kind {
	case "running":
		return "●"
	case "workspace":
		return "○"
	case "new":
		return "+"
	case "tools":
		return "?"
	default:
		return "›"
	}
}

func menuKindLabel(kind string) string {
	switch kind {
	case "running":
		return "RUNNING SESSION"
	case "workspace":
		return "WORKSPACE"
	case "new":
		return "NEW SESSION"
	case "tools":
		return "DIAGNOSTICS"
	default:
		return "ITEM"
	}
}

func menuMatches(items []menuItem, filter string) []int {
	indices := make([]int, 0, len(items))
	needle := strings.ToLower(menuText(filter))
	for i, item := range items {
		searchable := strings.ToLower(menuText(strings.Join([]string{item.Title, item.Detail, item.Workspace, item.Kind}, " ")))
		if needle == "" || strings.Contains(searchable, needle) {
			indices = append(indices, i)
		}
	}
	return indices
}

func emptyMenuLines(title string, width, height int, filter string, total int) []string {
	lines := []string{
		fitMenu("JARA · "+title, width),
		fitMenu(fmt.Sprintf("0 matches · %d total", total), width),
		fitMenu(menuSearchLabel(filter), width),
		fitMenu("No matches. Edit the filter or press Esc to clear.", width),
	}
	lines = append(lines, fitMenu("Backspace edits · Ctrl-U clears · Ctrl-C backs out", width))
	return lines
}
func selectMenu(title string, items []menuItem) (int, error) {
	if len(items) == 0 {
		return 0, io.EOF
	}
	if n, handled, err := terminalMenu(title, items); handled {
		return n, err
	}
	fmt.Println(menuText(title))
	for i, item := range items {
		fmt.Printf("  %d) %s — %s\n", i+1, menuText(item.Title), menuText(item.Detail))
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
	if name != "" && !nameIsID {
		return menuText(name)
	}
	if w.URL != "" {
		name := strings.SplitN(strings.SplitN(w.URL, "?", 2)[0], "#", 2)[0]
		name = name[strings.LastIndexAny(name, "/:")+1:]
		name = strings.TrimSuffix(name, ".git")
		if name != "" {
			return menuText(name)
		}
	}
	return "Copied workspace"
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

func workspaceBadge(w Workspace) string {
	id := strings.TrimPrefix(w.ID, "w-")
	if len(id) > 8 {
		id = id[:8]
	}
	if w.Branch != "" {
		return id + " · " + w.Branch
	}
	return id
}
func sessionItem(w Workspace, s Session, number int) menuItem {
	viewers := fmt.Sprintf("%d viewers", s.Attached)
	if s.Attached == 1 {
		viewers = "1 viewer"
	}
	if s.Attached == 0 {
		viewers = "detached"
	}
	return menuItem{Title: workspaceTitle(w), Detail: fmt.Sprintf("Running #%d · %s · %s", number, viewers, workspaceBadge(w)), Workspace: w.ID, Kind: "running"}
}
