package jara

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

type conversationProject struct {
	HistoryError error
	Label        string
	Workspace    Workspace
	Path         string
	Threads      []Conversation
	Terminals    []Session
	Recent       int64
}

func readBrowserWorkspaces() ([]Workspace, error) {
	files, err := filepath.Glob(filepath.Join(fsutil.DataRoot(), "workspaces", "*.json"))
	if err != nil {
		return nil, err
	}
	var workspaces []Workspace
	for _, file := range files {
		b, e := os.ReadFile(file)
		if e != nil {
			return nil, e
		}
		var w Workspace
		if e = json.Unmarshal(b, &w); e != nil {
			return nil, e
		}
		if w.Ready && w.URL != "" {
			w.Branch, _ = gitOutput(w.Path, "symbolic-ref", "--short", "-q", "HEAD")
		}
		workspaces = append(workspaces, w)
	}
	return workspaces, nil
}

func conversationProjects(workspaces []Workspace, threads []Conversation, terminals []Session) []conversationProject {
	projects := make([]conversationProject, 0, len(workspaces))
	byPath := map[string]int{}
	for _, w := range workspaces {
		byPath[filepath.Clean(w.Path)] = len(projects)
		projects = append(projects, conversationProject{Workspace: w, Path: w.Path})
	}
	for _, c := range threads {
		path := ""
		if c.CWD != "" {
			path = filepath.Clean(c.CWD)
		}
		index, ok := byPath[path]
		if !ok {
			best := 0
			for i, p := range projects {
				if p.Path != "" && strings.HasPrefix(path, filepath.Clean(p.Path)+string(os.PathSeparator)) && len(p.Path) > best {
					index, ok, best = i, true, len(p.Path)
				}
			}
		}
		if !ok {
			index = len(projects)
			byPath[path] = index
			projects = append(projects, conversationProject{Path: path})
		}
		p := &projects[index]
		p.Threads = append(p.Threads, c)
		p.Recent = max(p.Recent, c.Updated, c.Created)
	}
	for _, s := range terminals {
		for i := range projects {
			if s.Workspace != "" && projects[i].Workspace.ID == s.Workspace {
				projects[i].Terminals = append(projects[i].Terminals, s)
				break
			}
		}
	}
	for i := range projects {
		sort.SliceStable(projects[i].Threads, func(a, b int) bool {
			return max(projects[i].Threads[a].Updated, projects[i].Threads[a].Created) > max(projects[i].Threads[b].Updated, projects[i].Threads[b].Created)
		})
	}
	labelDuplicateProjects(projects)
	sort.SliceStable(projects, func(i, j int) bool {
		if projects[i].Recent != projects[j].Recent {
			return projects[i].Recent > projects[j].Recent
		}
		return projectName(projects[i]) < projectName(projects[j])
	})
	return projects
}
func projectName(p conversationProject) string {
	if p.Label != "" {
		return p.Label
	}
	return projectBaseName(p)
}

func projectBaseName(p conversationProject) string {
	if _, name, _, _, ok := workspaceOriginParts(p.Workspace.URL); ok {
		return name
	}
	if p.Workspace.Name != "" && !isGeneratedWorkspaceID(p.Workspace.Name) {
		return p.Workspace.Name
	}
	if p.Path == "" {
		return "Other conversations"
	}
	return filepath.Base(p.Path)
}
func projectDetail(p conversationProject) string {
	detail := fmt.Sprintf("%d chats", len(p.Threads))
	if p.HistoryError != nil {
		if len(p.Threads) == 0 {
			detail = "History unavailable"
		} else {
			detail = fmt.Sprintf("%d+ chats · history incomplete", len(p.Threads))
		}
	}
	if p.Workspace.Branch != "" {
		detail = p.Workspace.Branch + " · " + detail
	}
	if p.Workspace.ID != "" && !p.Workspace.Ready {
		detail += " · setup needed"
	}
	if len(p.Terminals) > 0 {
		detail += fmt.Sprintf(" · %d terminals", len(p.Terminals))
	}
	if _, _, host, owner, ok := workspaceOriginParts(p.Workspace.URL); ok {
		detail += " · " + owner + "@" + host
	}
	if p.Workspace.SourcePath != "" {
		detail += " · " + p.Workspace.SourcePath
	}
	return detail
}

// Each level has one job: choose a project, then a conversation or terminal.
// Starting something new comes first, since a phone keyboard makes search slow.
func browseProjects() (menuItem, error) {
	selectedWorkspace, selectedPath, selected := "", "", 0
	for {
		data, err := loadProjectBrowser()
		if err != nil {
			return menuItem{}, err
		}
		projects, terminals, historyErr := data.Projects, data.Terminals, data.HistoryError
		items := []menuItem{{Title: "New conversation", Detail: "Start an agent in a project", Kind: "new-conversation"}}
		for _, p := range projects {
			items = append(items, menuItem{Title: projectName(p), Detail: projectDetail(p), Kind: "project"})
		}
		if devices := onlineDeviceNames(); len(devices) > 0 {
			items = append(items, menuItem{Title: "Devices", Detail: strings.Join(devices, ", "), Kind: "devices"})
		}
		if hosts := otherNetworkHosts(); len(hosts) > 0 {
			names := make([]string, len(hosts))
			for i, h := range hosts {
				names[i] = h.Name
			}
			items = append(items, menuItem{Title: "Other hosts", Detail: strings.Join(names, ", "), Kind: "hosts"})
		}
		detail := "Codex's native picker"
		if historyErr != nil {
			detail = "History unavailable: " + historyErr.Error()
		}
		items = append(items, menuItem{Title: "All Codex conversations", Detail: detail, Kind: "history"}, menuItem{Title: "Terminals", Detail: fmt.Sprintf("%d terminal records", len(terminals)), Kind: "terminals"}, menuItem{Title: "Diagnostics", Detail: "Host health", Kind: "diagnostics"}, menuItem{Title: "Refresh projects", Detail: "Reload chats and running terminals", Kind: "refresh"})
		if selectedWorkspace != "" || selectedPath != "" {
			selected = 1 + projectBrowserSelection(projects, selectedWorkspace, selectedPath)
		}
		n, e := selectMenuAt("Projects", items, selected)
		if e != nil {
			return menuItem{}, e
		}
		selectedWorkspace, selectedPath, selected = "", "", n
		var chosen menuItem
		switch items[n].Kind {
		case "history":
			return items[n], nil
		case "refresh":
			continue
		case "diagnostics":
			e = showHostDiagnostics()
		case "terminals":
			chosen, e = browseTerminals("Terminals", terminals)
		case "new-conversation":
			chosen, e = chooseNewConversation(projects)
		case "devices":
			chosen, e = browseDevices()
		case "hosts":
			chosen, e = browseNetworkHosts()
		case "project":
			p := projects[n-1]
			selectedWorkspace, selectedPath = p.Workspace.ID, p.Path
			chosen, e = browseProject(p, terminals)
		}
		if errors.Is(e, io.EOF) || e == nil && chosen.Kind == "" {
			continue
		}
		return chosen, e
	}
}

func browseProject(p conversationProject, terminals []Session) (menuItem, error) {
	selectedConversation := 0
	for {
		rows := newConversationRows(p)
		if p.HistoryError != nil {
			rows = append(rows, menuItem{Title: "Open Codex history", Detail: "History unavailable: " + p.HistoryError.Error(), Kind: "history"})
		}
		for _, c := range p.Threads {
			title := strings.TrimSpace(c.Name)
			if title == "" {
				title = strings.Join(strings.Fields(c.Preview), " ")
			}
			if title == "" {
				title = "Untitled conversation"
			}
			live := 0
			for _, t := range terminals {
				if t.Conversation == c.ID && !t.Dead {
					live++
				}
			}
			detail := c.Agent
			if live > 0 {
				detail += fmt.Sprintf(" · %d live", live)
			}
			if c.Status.Type == "active" {
				detail += " · working"
			}
			detail += " · " + conversationAge(c.Updated)
			rows = append(rows, menuItem{Title: title, Detail: detail, Workspace: p.Workspace.ID, Path: c.CWD, Kind: "conversation", Action: c.ID, Agent: c.Agent})
		}
		if len(p.Terminals) > 0 {
			rows = append(rows, menuItem{Title: "Project terminals", Detail: fmt.Sprintf("%d terminals", len(p.Terminals)), Kind: "terminals"})
		}
		rows = append(rows, menuItem{Title: "Back to projects", Kind: "back"})
		i, e := selectMenuAt(projectName(p), rows, selectedConversation)
		if e != nil {
			if errors.Is(e, io.EOF) {
				return menuItem{}, io.EOF
			}
			return menuItem{}, e
		}
		selectedConversation = i
		if rows[i].Kind == "back" {
			return menuItem{}, io.EOF
		}
		if rows[i].Kind == "terminals" {
			chosen, e := browseTerminals(projectName(p)+" · terminals", p.Terminals)
			if e != nil {
				if errors.Is(e, io.EOF) {
					continue
				}
				return menuItem{}, e
			}
			return chosen, nil
		}
		if rows[i].Kind == "conversation" {
			var live []Session
			for _, t := range terminals {
				if t.Conversation == rows[i].Action && !t.Dead {
					live = append(live, t)
				}
			}
			if len(live) == 1 {
				return sessionItem(p.Workspace, live[0], 1), nil
			}
			if len(live) > 0 {
				chosen, e := browseTerminals("Live terminals", live)
				if errors.Is(e, io.EOF) {
					continue
				}
				return chosen, e
			}
		}
		return rows[i], nil
	}
}

func browseTerminals(title string, terminals []Session) (menuItem, error) {
	rows := make([]menuItem, 0, len(terminals))
	for i, t := range terminals {
		item := sessionItem(Workspace{ID: t.Workspace}, t, i+1)
		rows = append(rows, item)
	}
	n, err := selectMenu(title, rows)
	if err != nil {
		return menuItem{}, err
	}
	return rows[n], nil
}

func conversationAge(at int64) string {
	if at == 0 {
		return "saved"
	}
	age := time.Since(time.Unix(at, 0))
	if age < time.Hour {
		return "recent"
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh", int(age.Hours()))
	}
	return fmt.Sprintf("%dd", int(age.Hours()/24))
}
