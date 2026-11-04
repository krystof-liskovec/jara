package jara

func init() {
	entry := commandHelp["config"]
	entry.usage = "jara config set|codex-args|git-identity ..."
	entry.examples = append(entry.examples, "jara config git-identity show", "jara config git-identity set --local --name 'Jane Doe' --email jane@example.com")
	commandHelp["config"] = entry
	commandHelp["config git-identity"] = helpEntry{
		usage:   "jara config git-identity show|set|clear [OPTIONS]",
		summary: "Manage the Git author name and email for a remote or this environment.",
		commands: []string{
			"show\tShow the global identity without exposing other Git configuration.",
			"set\tSet both the Git author name and email.",
			"clear\tRemove the managed identity and restore prior defaults.",
		},
		examples: []string{
			"jara config git-identity show --remote jellyfish",
			"jara config git-identity set --remote jellyfish --name 'Jane Doe' --email jane@example.com",
			"jara config git-identity set --local --name 'Jane Doe' --email jane@example.com",
			"jara config git-identity clear --remote jellyfish",
		},
	}
	commandHelp["config git-identity show"] = gitIdentityHelpEntry("show", "Show the global Git author name and email, including Git includes.", false)
	commandHelp["config git-identity set"] = gitIdentityHelpEntry("set", "Set both values for the selected scope.", true)
	commandHelp["config git-identity clear"] = gitIdentityHelpEntry("clear", "Remove the managed identity and restore previous Git defaults.", false)
}

func gitIdentityHelpEntry(action, summary string, set bool) helpEntry {
	entry := helpEntry{
		usage:   "jara config git-identity " + action + " [OPTIONS]",
		summary: summary,
		options: []string{
			"--remote NAME   Select a configured remote (default: selected remote).",
			"--local         Configure this environment instead of a remote.",
		},
		examples: []string{
			"jara config git-identity " + action + " --remote jellyfish",
			"jara config git-identity " + action + " --local",
		},
	}
	if set {
		entry.options = append(entry.options,
			"--name NAME     Git author name (required).",
			"--email EMAIL   Git author email (required).",
		)
		entry.examples = []string{
			"jara config git-identity set --remote jellyfish --name 'Jane Doe' --email jane@example.com",
			"jara config git-identity set --local --name 'Jane Doe' --email jane@example.com",
		}
	}
	return entry
}
