package jara

import (
	"errors"
	"io"

	"krystof.liskovec/jara/internal/protocol"
)

// newConversationRows offers one new conversation per installed agent at the
// top of a project, so starting one never needs paging past its history.
func newConversationRows(p conversationProject) []menuItem {
	if !p.Workspace.Ready {
		return []menuItem{}
	}
	var rows []menuItem
	for _, runtime := range installedAgentRuntimes() {
		rows = append(rows, menuItem{Title: "New " + protocol.AgentDisplayName(runtime) + " conversation", Detail: "Start an agent terminal", Workspace: p.Workspace.ID, Kind: "new", Agent: runtime})
	}
	return rows
}

// chooseNewConversation is the Projects menu's shortcut: pick a project the
// host has files for, then the agent when more than one is installed.
func chooseNewConversation(projects []conversationProject) (menuItem, error) {
	var ready []conversationProject
	rows := []menuItem{}
	for _, p := range projects {
		if p.Workspace.Ready {
			ready = append(ready, p)
			rows = append(rows, menuItem{Title: projectName(p), Detail: projectDetail(p), Kind: "project"})
		}
	}
	if len(ready) == 0 {
		return menuItem{}, showMenuNotice("New conversation",
			"No project is ready on this host",
			"Attach a folder from a computer: jara --remote NAME")
	}
	for {
		n, err := selectMenu("New conversation in…", rows)
		if err != nil {
			return menuItem{}, err
		}
		agents := newConversationRows(ready[n])
		if len(agents) == 1 {
			return agents[0], nil
		}
		i, err := selectMenu(projectName(ready[n])+" · agent", agents)
		if errors.Is(err, io.EOF) {
			continue
		}
		if err != nil {
			return menuItem{}, err
		}
		return agents[i], nil
	}
}

// showMenuNotice shows a short message as a menu, which any key leaves.
func showMenuNotice(title string, lines ...string) error {
	rows := make([]menuItem, len(lines))
	for i, line := range lines {
		rows[i] = menuItem{Title: line, Kind: "notice"}
	}
	_, err := selectMenu(title, rows)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
