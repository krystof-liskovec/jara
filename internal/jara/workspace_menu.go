package jara

import (
	"encoding/base64"
	"sort"
	"strings"
	"time"
)

func workspaceMenuItems(workspaces []Workspace, sessions []Session) []menuItem {
	ordered := append([]Workspace(nil), workspaces...)
	known := make(map[string]bool, len(workspaces))
	byWorkspace := make(map[string][]Session)
	var otherSessions []Session
	latest := make(map[string]int64)
	for _, workspace := range workspaces {
		known[workspace.ID] = true
	}
	for _, session := range sessions {
		if session.Workspace == "" || !known[session.Workspace] {
			otherSessions = append(otherSessions, session)
			continue
		}
		byWorkspace[session.Workspace] = append(byWorkspace[session.Workspace], session)
		if at := sessionRecency(session); at > latest[session.Workspace] {
			latest[session.Workspace] = at
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := workspaceRecency(ordered[i], latest), workspaceRecency(ordered[j], latest)
		if left != right {
			return left > right
		}
		return strings.ToLower(workspaceTitle(ordered[i])) < strings.ToLower(workspaceTitle(ordered[j]))
	})

	items := make([]menuItem, 0, len(ordered)*3+len(sessions))
	for _, workspace := range ordered {
		groupSessions := append([]Session(nil), byWorkspace[workspace.ID]...)
		sort.SliceStable(groupSessions, func(i, j int) bool {
			return sessionRecency(groupSessions[i]) > sessionRecency(groupSessions[j])
		})
		detail := "Open workspace · " + workspaceBadge(workspace)
		latestSession := ""
		if len(groupSessions) > 0 {
			latestSession = groupSessions[0].Name
		}
		if !workspace.Ready {
			detail = "Setup unfinished · " + workspaceBadge(workspace)
		} else if len(groupSessions) > 0 {
			detail = "Open latest terminal · " + workspaceBadge(workspace)
		} else {
			detail = "No Jara terminals · Open workspace · " + workspaceBadge(workspace)
		}
		items = append(items, menuItem{Title: workspaceTitle(workspace), Detail: detail, Workspace: workspace.ID, Kind: "workspace", Action: latestSession})
		for i, session := range groupSessions {
			item := sessionItem(workspace, session, i+1)
			item.Indent = 1
			items = append(items, item)
		}
		if workspace.Ready {
			items = append(items, menuItem{Title: "New session", Detail: "Start a new Codex terminal", Workspace: workspace.ID, Group: workspaceTitle(workspace), Kind: "new", Indent: 1})
		}
	}
	if len(otherSessions) > 0 {
		sort.SliceStable(otherSessions, func(i, j int) bool {
			return sessionRecency(otherSessions[i]) > sessionRecency(otherSessions[j])
		})
		items = append(items, menuItem{
			Title:  "Other terminals",
			Detail: "Workspace missing or unlinked",
			Kind:   "group",
			Action: otherSessions[0].Name,
		})
		for i, session := range otherSessions {
			item := sessionItem(Workspace{ID: session.Workspace, Name: "Other terminals"}, session, i+1)
			item.Group = "Other terminals"
			item.Indent = 1
			items = append(items, item)
		}
	}
	return items
}

func sessionRecency(session Session) int64 {
	if session.LastAttach != 0 {
		return session.LastAttach
	}
	return session.CreatedAt
}

func workspaceRecency(workspace Workspace, latest map[string]int64) int64 {
	if latest[workspace.ID] != 0 {
		return latest[workspace.ID]
	}
	created, err := time.Parse(time.RFC3339, workspace.Created)
	if err != nil {
		return 0
	}
	return created.UnixNano()
}

func sessionMenuLabel(request Request) string {
	switch {
	case request.Background:
		prompt := strings.ReplaceAll(menuText(request.Prompt), "|", "/")
		prompt = strings.Join(strings.Fields(prompt), " ")
		if prompt == "" {
			return "Background Codex run"
		}
		return "Run: " + fitMenu(prompt, 70)
	case request.ResumeLatest:
		return "Resume latest Codex"
	case request.Worktree:
		return "Worktree session"
	default:
		return ""
	}
}

func encodeSessionMenuLabel(label string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(label))
}

func decodeSessionMenuLabel(encoded string) string {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return string(decoded)
}
