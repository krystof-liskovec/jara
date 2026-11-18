package jara

// Help for where an attached workspace runs its agent is layered onto the
// session commands.
func init() {
	options := []string{
		"--local             Attached folders: run the agent on this device (the default when Codex is installed here).",
		"--run-remote        Run the agent on the remote host (attaching the folder to the default remote); JARA_RUN_REMOTE=1 makes it the default for attached folders.",
		"--take-over         Take the conversation although another device still looks online.",
	}
	for _, key := range []string{"root", "open", "resume", "new"} {
		entry := commandHelp[key]
		entry.options = append(entry.options, options...)
		commandHelp[key] = entry
	}
	addHelpExamples("root", "jara --run-remote", "jara runner status")
	commandHelp["config set host-name"] = helpEntry{
		usage:    "jara config set host-name NAME|auto",
		summary:  "Run on a host. Names it in the controls of chat windows that run on it, as after ssh and jara connect. Without it the host uses the name your Jara client calls it by, learned when you open sessions there; auto returns to that.",
		examples: []string{"jara config set host-name jellyfish", "jara config set host-name auto"},
	}
	commandHelp["remote order"] = helpEntry{
		usage:    "jara remote order [NAME...]",
		summary:  "Show or set the ranking of remote hosts; the first is the default and primary host. Unlisted remotes keep their order after the named ones.",
		examples: []string{"jara remote order", "jara remote order jellyfish backup"},
	}
	commandHelp["remote standby"] = helpEntry{
		usage: "jara remote standby NAME [--off] [--address HOST[:PORT]]",
		summary: "Make a remote a standby: the primary (rank 1) forwards every checkpoint to it over SSH, with a key that may only run Jara's standby operations. " +
			"--address is how other hosts reach it, when your SSH configuration uses an alias, jump host or tunnel they cannot.",
		examples: []string{"jara remote standby backup", "jara remote standby backup --address backup.example.org:2222", "jara remote standby backup --off"},
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
