package jara

func init() {
	commandHelp["usage"] = helpEntry{
		usage:    "jara usage [--remote NAME]",
		summary:  "Show the Codex and Claude Code usage limits on this device and every remote.",
		options:  []string{"--remote NAME  Show only this host."},
		examples: []string{"jara usage", "jara usage --remote jellyfish"},
	}
	commandHelp["update"] = helpEntry{
		usage:    "jara update",
		summary:  "Check now for a newer Jara on the remotes and newer Codex and Claude Code here and there, and offer to update them. Opening a conversation offers the same from a check made at most every six hours; JARA_NO_UPDATE_CHECK=1 turns that off.",
		examples: []string{"jara update"},
	}
	commandHelp["clone"] = helpEntry{
		usage:    "jara clone [--remote NAME] REPOSITORY_URL",
		summary:  "Clone a Git repository on a Jara host and open it in Codex.",
		options:  []string{"--remote NAME  Select the Jara host."},
		examples: []string{"jara clone https://gitlab.com/group/project.git", "jara clone --remote work https://git.example/team/project.git"},
	}
}
