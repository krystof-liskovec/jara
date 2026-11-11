package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
)

// Labels are derived from persistent workspace data, so activity sorting never
// changes which checkout a label identifies.
func labelDuplicateProjects(projects []conversationProject) {
	groups := map[string][]int{}
	for i := range projects {
		projects[i].Label = ""
		name := strings.ToLower(textutil.SanitizeText(projectBaseName(projects[i])))
		groups[name] = append(groups[name], i)
	}
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		labels := map[string]int{}
		for _, i := range indices {
			p := &projects[i]
			detail := projectCheckoutLabel(*p, projects, indices)
			p.Label = projectBaseName(*p) + " · " + detail
			labels[strings.ToLower(textutil.SanitizeText(p.Label))]++
		}
		for _, i := range indices {
			p := &projects[i]
			if labels[strings.ToLower(textutil.SanitizeText(p.Label))] > 1 {
				p.Label += " · " + projectCheckoutReference(*p)
			}
		}
	}
}

func projectCheckoutLabel(p conversationProject, projects []conversationProject, indices []int) string {
	if host := p.Workspace.SourceHost; host != "" {
		count := 0
		for _, i := range indices {
			if projects[i].Workspace.SourceHost == host {
				count++
			}
		}
		if count == 1 || p.Workspace.SourcePath == "" {
			return host
		}
		return host + ":" + projectSourceSuffix(p, projects, indices)
	}
	if p.Workspace.SourcePath != "" {
		return p.Workspace.SourcePath
	}
	if created, err := time.Parse(time.RFC3339, p.Workspace.Created); err == nil {
		return "created " + created.UTC().Format("2006-01-02 15:04Z")
	}
	if p.Workspace.ID == "" && p.Path != "" {
		return p.Path
	}
	return "checkout " + projectCheckoutReference(p)
}

func projectSourceSuffix(p conversationProject, projects []conversationProject, indices []int) string {
	parts := sourcePathParts(p.Workspace.SourcePath)
	for depth := 1; depth <= len(parts); depth++ {
		suffix := strings.Join(parts[len(parts)-depth:], "/")
		unique := true
		for _, i := range indices {
			other := projects[i].Workspace
			if other.ID == p.Workspace.ID || other.SourceHost != p.Workspace.SourceHost {
				continue
			}
			otherParts := sourcePathParts(other.SourcePath)
			if len(otherParts) >= depth && strings.Join(otherParts[len(otherParts)-depth:], "/") == suffix {
				unique = false
				break
			}
		}
		if unique {
			return suffix
		}
	}
	return p.Workspace.SourcePath
}

func sourcePathParts(path string) []string {
	return strings.Split(strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/"), "/")
}

func projectCheckoutReference(p conversationProject) string {
	if id := strings.TrimPrefix(p.Workspace.ID, "w-"); id != "" {
		if len(id) > 8 {
			return id[len(id)-8:]
		}
		return id
	}
	sum := sha256.Sum256([]byte(p.Path))
	return hex.EncodeToString(sum[:4])
}
