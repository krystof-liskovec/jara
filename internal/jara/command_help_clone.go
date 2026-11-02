package jara

func init() {
	commandHelp["clone"] = helpEntry{
		usage:    "jara clone [--remote NAME] REPOSITORY_URL",
		summary:  "Clone a Git repository on a Jara host and open it in Codex.",
		options:  []string{"--remote NAME  Select the Jara host."},
		examples: []string{"jara clone https://gitlab.com/group/project.git", "jara clone --remote work https://git.example/team/project.git"},
	}
}
