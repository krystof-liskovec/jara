package jara

import (
	"fmt"
	"strings"
)

type existingCheckoutChoice struct {
	Binding Binding
	Clone   bool
}

func repositoryMatchKey(origin string) string {
	if validateOrigin(origin) != nil {
		return ""
	}
	if target, err := parseGitCredentialTarget(origin); err == nil {
		return target.Host + "/" + strings.TrimSuffix(strings.TrimRight(target.Path, "/"), ".git")
	}
	return origin
}

func existingCheckoutOptions(workspaces []Workspace, origin string) ([]Workspace, []menuItem) {
	key := repositoryMatchKey(origin)
	var matches []Workspace
	if key != "" {
		for _, w := range workspaces {
			if w.Ready && nameRE.MatchString(w.ID) && repositoryMatchKey(w.URL) == key {
				matches = append(matches, w)
			}
		}
	}
	projects := conversationProjects(matches, nil, nil)
	items := make([]menuItem, 0, len(projects)+1)
	matches = matches[:0]
	for _, p := range projects {
		matches = append(matches, p.Workspace)
		items = append(items, menuItem{Title: "Use " + projectName(p), Detail: projectDetail(p), Kind: "workspace"})
	}
	if len(matches) > 0 {
		items = append(items, menuItem{Title: "Clone a separate checkout", Detail: "Create a workspace for this local folder", Kind: "new"})
	}
	return matches, items
}

func offerExistingCheckout(r Remote, remoteName, root, origin string) (existingCheckoutChoice, error) {
	var workspaces []Workspace
	if err := rpc(r, Request{Op: "workspace-list"}, nil, &workspaces); err != nil {
		return existingCheckoutChoice{}, fmt.Errorf("check for existing workspaces: %w; use --clone to skip workspace discovery", err)
	}
	matches, items := existingCheckoutOptions(workspaces, origin)
	if len(matches) == 0 {
		return existingCheckoutChoice{}, nil
	}
	selected, err := selectMenu("Repository already on "+remoteName, items)
	if err != nil {
		return existingCheckoutChoice{}, err
	}
	if selected == len(matches) {
		return existingCheckoutChoice{Clone: true}, nil
	}
	w := matches[selected]
	// Recheck readiness and origin after selection before linking a local folder.
	var current Workspace
	if err = rpc(r, Request{Op: "info", ID: w.ID}, nil, &current); err != nil {
		return existingCheckoutChoice{}, err
	}
	if !current.Ready || current.ID != w.ID || repositoryMatchKey(current.URL) != repositoryMatchKey(origin) {
		return existingCheckoutChoice{}, fmt.Errorf("selected workspace no longer matches this repository; reopen Jara to retry")
	}
	binding := Binding{Remote: remoteName, Workspace: w.ID}
	if err = seedExistingBaseline(binding, root); err != nil {
		return existingCheckoutChoice{}, fmt.Errorf("prepare synchronization for the existing checkout: %w", err)
	}
	fmt.Printf("Using the existing checkout on %s.\n", remoteName)
	return existingCheckoutChoice{Binding: binding}, nil
}
