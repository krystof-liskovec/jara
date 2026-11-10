package jara

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

const maxHandoffSection = 6000

// handoffTarget chooses the receiving agent when the user did not name one:
// the agent that did not run most recently in this workspace.
func handoffTarget(workspace string) string {
	if handoffSource(workspace) == agentClaude {
		return agentCodex
	}
	return agentClaude
}

// handoffSource reports the agent of the newest session in a workspace.
// Sessions created before agent tagging ran Codex.
func handoffSource(workspace string) string {
	all, err := sessions()
	if err != nil {
		return agentCodex
	}
	var matches []Session
	for _, s := range all {
		if s.Workspace == workspace && !s.ChatDaemon {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	sort.SliceStable(matches, func(i, j int) bool { return sessionRecency(matches[i]) > sessionRecency(matches[j]) })
	if matches[0].Agent == "" {
		return agentCodex
	}
	return matches[0].Agent
}

// handoffPrompt gives the receiving agent the shared state it needs. Agent
// conversations use incompatible formats, so the repository is the source of
// truth and the previous agent's history is not copied.
func handoffPrompt(path, from, to, note string) string {
	var b strings.Builder
	source := "another agent"
	if from != "" {
		source = agentDisplayName(from)
	}
	fmt.Fprintf(&b, "You are taking over work in this workspace from %s through a Jara handoff. ", source)
	b.WriteString("Its conversation history is not available; the working tree below is the current state, including uncommitted changes. ")
	b.WriteString("Start by reviewing the changes, then summarize what you believe was in progress and continue. Ask if the goal is unclear.\n")
	if strings.TrimSpace(note) != "" {
		fmt.Fprintf(&b, "\nHandoff note from the user:\n%s\n", strings.TrimSpace(note))
	}
	sections := []struct {
		title string
		args  []string
	}{
		{"Branch", []string{"rev-parse", "--abbrev-ref", "HEAD"}},
		{"Status", []string{"status", "--short"}},
		{"Uncommitted changes (stat)", []string{"diff", "HEAD", "--stat"}},
		{"Recent commits", []string{"log", "--oneline", "-n", "10"}},
	}
	for _, section := range sections {
		cmd := exec.Command("git", append([]string{"-C", path}, section.args...)...)
		out, err := cmd.Output()
		if err != nil {
			if section.title == "Branch" {
				b.WriteString("\nThis workspace is not a Git repository; inspect the files directly.\n")
				break
			}
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for i, line := range lines {
			lines[i] = menuText(line)
		}
		text := strings.Join(lines, "\n")
		if text == "" {
			text = "(none)"
		}
		if len(text) > maxHandoffSection {
			text = text[:maxHandoffSection] + "\n… (truncated)"
		}
		fmt.Fprintf(&b, "\n%s:\n%s\n", section.title, text)
	}
	if tasks := handoffTaskSummary(path); tasks != "" {
		fmt.Fprintf(&b, "\nDelegated agent tasks started from this workspace:\n%s\n", tasks)
	}
	fmt.Fprintf(&b, "\nYou are %s.", agentDisplayName(to))
	return b.String()
}
