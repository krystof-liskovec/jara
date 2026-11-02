package jara

type projectBrowserData struct {
	Projects     []conversationProject
	Terminals    []Session
	HistoryError error
}

func loadProjectBrowser() (projectBrowserData, error) {
	workspaces, err := readBrowserWorkspaces()
	if err != nil {
		return projectBrowserData{}, err
	}
	terminals, err := sessions()
	if err != nil {
		return projectBrowserData{}, err
	}
	threads, historyErr := loadConversations()
	linkConversationTerminals(threads, terminals)
	return projectBrowserState(workspaces, threads, terminals, historyErr), nil
}

func projectBrowserState(workspaces []Workspace, threads []Conversation, terminals []Session, historyErr error) projectBrowserData {
	projects := conversationProjects(workspaces, threads, terminals)
	for i := range projects {
		projects[i].HistoryError = historyErr
	}
	return projectBrowserData{Projects: projects, Terminals: terminals, HistoryError: historyErr}
}

func projectBrowserSelection(projects []conversationProject, id, path string) int {
	for i, p := range projects {
		if id != "" && p.Workspace.ID == id || id == "" && path != "" && p.Path == path {
			return i
		}
	}
	return 0
}
