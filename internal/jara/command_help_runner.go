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
	commandHelp["remote order"] = helpEntry{
		usage:    "jara remote order [NAME...]",
		summary:  "Show or set the ranking of remote hosts; the first is the default and primary host. Unlisted remotes keep their order after the named ones.",
		examples: []string{"jara remote order", "jara remote order jellyfish backup"},
	}
	commandHelp["runner"] = helpEntry{
		usage: "jara runner status|prefer ...",
		summary: "Show which machine runs a workspace's agent, whether it is online, and its last checkpoint; " +
			"or rank the devices that get a conversation back from the remote host when several windows wait for it.",
		commands: []string{
			"status [WORKSPACE_ID]\tWhere the agent runs, waiting devices, preferences.",
			"prefer [DEVICE...|--none]\tRank devices for hand-back; the remote host never starts an agent on a device.",
		},
		options: []string{"--remote NAME       Select the Jara host.", "--workspace ID      prefer: the workspace (default: this folder's)."},
		examples: []string{"jara runner status", "jara runner status w-0123456789abcdef --remote work",
			"jara runner prefer laptop desktop", "jara runner prefer --none"},
	}
}
