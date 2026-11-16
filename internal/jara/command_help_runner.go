package jara

// Local-runner help is layered onto the session commands, like --agent.
func init() {
	local := "--local             Run the agent on this device; the remote keeps files and conversation after every turn."
	takeOver := "--take-over         Continue from the last checkpoint although the device running the agent is offline."
	for _, key := range []string{"root", "open", "resume", "new"} {
		entry := commandHelp[key]
		entry.options = append(entry.options, local, takeOver)
		commandHelp[key] = entry
	}
	connect := commandHelp["connect"]
	connect.options = append(connect.options, takeOver)
	commandHelp["connect"] = connect
	addHelpExamples("root", "jara --local", "jara --local --take-over")
	commandHelp["runner"] = helpEntry{
		usage:    "jara runner status [WORKSPACE_ID] [--remote NAME]",
		summary:  "Show which device runs a workspace's agent, whether it is online, and its last checkpoint.",
		options:  []string{"--remote NAME       Select the Jara host."},
		examples: []string{"jara runner status", "jara runner status w-0123456789abcdef --remote work"},
	}
}
