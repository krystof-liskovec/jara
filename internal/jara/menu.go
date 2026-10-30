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

type menuItem struct{ Title, Detail, Workspace string }

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
	lines := 1
	if width < 60 {
		lines = 2
	}
	return max(1, min(9, (height-5)/lines))
}

// Each line is bounded in terminal cells, leaving the last column unused to
// prevent automatic wrapping. Short screens retain navigation and pagination.
func menuLines(title string, items []menuItem, selected, width, height int) []string {
	width = max(1, width-1)
	if height < 7 || width+1 < 16 {
		return []string{fitMenu("Enlarge terminal; q to exit", width)}
	}
	size := menuPageSize(width+1, height)
	start := selected / size * size
	end := min(start+size, len(items))
	lines := []string{fitMenu("Jara · "+title, width), fitMenu(fmt.Sprintf("%d–%d of %d", start+1, end, len(items)), width)}
	for i := start; i < end; i++ {
		mark := "  "
		if i == selected {
			mark = "> "
		}
		prefix := fmt.Sprintf("%s%d ", mark, i-start+1)
		if width+1 < 60 {
			lines = append(lines, fitMenu(prefix+items[i].Title, width), fitMenu("    "+items[i].Detail, width))
		} else {
			titleCells := min(40, width/2)
			name := fitMenu(items[i].Title, titleCells)
			padding := strings.Repeat(" ", max(1, titleCells-uniseg.StringWidth(name)+2))
			lines = append(lines, fitMenu(prefix+name+padding+items[i].Detail, width))
		}
	}
	identity := ""
	if items[selected].Workspace != "" {
		identity = fitMenu("ID: "+items[selected].Workspace, width)
	}
	openHelp, moveHelp := "1-9 open | j/k move", "Enter open | n/p page | q back"
	if width < 29 {
		openHelp, moveHelp = "1-9/Enter open", "j/k n/p q:back"
	}
	lines = append(lines, identity, fitMenu(openHelp, width), fitMenu(moveHelp, width))
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
	if strings.TrimSpace(w.Name) != "" {
		return menuText(w.Name)
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
	return menuItem{Title: workspaceTitle(w), Detail: fmt.Sprintf("Running #%d · %s · %s", number, viewers, workspaceBadge(w)), Workspace: w.ID}
}
