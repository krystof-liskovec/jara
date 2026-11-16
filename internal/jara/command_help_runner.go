package jara

// Help for agents running on devices is layered onto the session commands.
func init() {
	options := []string{
		"--local             Run the agent on this device (the default when Codex is installed here).",
		"--run-remote        Run the agent on the remote host; JARA_RUN_REMOTE=1 makes it the default.",
		"--take-over         Take the conversation although another device still looks online.",
	}
	for _, key := range []string{"root", "open", "resume", "new"} {
		entry := commandHelp[key]
		entry.options = append(entry.options, options...)
		commandHelp[key] = entry
	}
	addHelpExamples("root", "jara --run-remote", "jara runner status")
	commandHelp["runner"] = helpEntry{
		usage:    "jara runner status [WORKSPACE_ID] [--remote NAME]",
		summary:  "Show which machine runs a workspace's agent, whether it is online, and its last checkpoint.",
		options:  []string{"--remote NAME       Select the Jara host."},
		examples: []string{"jara runner status", "jara runner status w-0123456789abcdef --remote work"},
	}
}
