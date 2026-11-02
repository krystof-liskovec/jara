package jara

func init() {
	commandHelp["docker cleanup"] = helpEntry{
		usage:    "jara docker cleanup [--remote NAME] [--older-than DURATION]",
		summary:  "Remove stopped nested Docker containers after reviewing them.",
		options:  []string{"--remote NAME       Select the Jara host.", "--older-than TIME   Minimum stopped age, from 1h to 8760h; default 24h."},
		examples: []string{"jara docker cleanup", "jara docker cleanup --remote work --older-than 72h"},
	}
}
