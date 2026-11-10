package jara

// Claude Code help is layered onto the Codex-oriented entries so each command
// documents --agent without duplicating its whole definition.
func init() {
	agentOption := "--agent NAME        Agent runtime: codex (default) or claude."
	for _, key := range []string{"root", "open", "resume", "run", "new", "connect"} {
		entry := commandHelp[key]
		entry.options = append(entry.options, agentOption)
		commandHelp[key] = entry
	}
	addHelpExamples("root", "jara --agent claude", "jara new --agent claude -- --model opus")
	addHelpExamples("run", "jara run --agent claude 'fix the failing tests'")
	addHelpExamples("connect", "jara connect --agent claude")

	auth := commandHelp["auth"]
	auth.usage = "jara auth codex|claude|git|gitlab|gitea [OPTIONS]"
	auth.summary = "Configure remote Codex or Claude Code sign-in, or Git credentials."
	auth.examples = append(auth.examples, "jara auth claude --remote jellyfish")
	commandHelp["auth"] = auth
	commandHelp["auth claude"] = helpEntry{
		usage:   "jara auth claude [--remote NAME] [--token-stdin]",
		summary: "Store a Claude token on a remote host. Create it with `claude setup-token` (an Anthropic API key also works); Jara asks for it with hidden input.",
		options: []string{
			"--remote NAME   Select a configured host.",
			"--token-stdin   Read the token from standard input instead of prompting.",
		},
		examples: []string{
			"jara auth claude --remote jellyfish",
			"printf '%s' \"$CLAUDE_CODE_OAUTH_TOKEN\" | jara auth claude --remote jellyfish --token-stdin",
		},
	}

	config := commandHelp["config"]
	config.examples = append(config.examples, "jara config set agent claude")
	commandHelp["config"] = config
	commandHelp["config set agent"] = helpEntry{
		usage:    "jara config set agent codex|claude|default [--remote NAME | --workspace]",
		summary:  "Choose the default agent globally, for one remote, or for the current folder; default removes a remote or folder override. --agent always wins.",
		options:  []string{"--remote NAME   Save the default for one remote.", "--workspace     Save the default for the current folder."},
		examples: []string{"jara config set agent claude", "jara config set agent codex --workspace", "jara config set agent default --remote work"},
	}
	commandHelp["config claude-args"] = helpEntry{
		usage:    "jara config claude-args show|set|clear [--remote NAME] [-- CLAUDE_ARGS...]",
		summary:  "Manage default Claude Code arguments on a host, like codex-args does for Codex. Deployments can also set JARA_CLAUDE_ARGS.",
		options:  []string{"--remote NAME   Select a configured host."},
		examples: []string{"jara config claude-args set --remote work -- --model opus", "jara config claude-args show"},
	}
	commandHelp["handoff"] = helpEntry{
		usage:    "jara handoff [--agent codex|claude] [--note TEXT] [WORKSPACE OPTIONS] [-- AGENT_ARGS...]",
		summary:  "Continue the current workspace with another agent. Jara starts it with a briefing built from the Git state, pending delegated tasks and your note. Without --agent it picks the agent that did not run most recently.",
		options:  []string{"--agent NAME    Receiving agent.", "--note TEXT     Extra instructions for the receiving agent.", "--remote NAME   Select a configured host."},
		examples: []string{"jara handoff", "jara handoff --agent claude --note 'finish the parser tests'"},
	}
	commandHelp["delegation"] = helpEntry{
		usage:    "jara delegation enable|disable|status",
		summary:  "Run on a Jara host. Controls the jara MCP server that lets Codex and Claude Code delegate tasks to each other. It is enabled by default and registered when a session starts.",
		examples: []string{"jara delegation status", "jara delegation disable"},
	}
	commandHelp["mcp"] = helpEntry{
		usage:    "jara mcp",
		summary:  "MCP stdio server started by Codex and Claude Code. It provides delegate_task, task_status, continue_task, list_tasks, merge_task and cancel_task.",
		examples: []string{"codex mcp list", "claude mcp list"},
	}
	root := commandHelp["root"]
	root.commands = append(root.commands, "handoff, delegation, mcp\tSwitch agents and let them delegate to each other.")
	commandHelp["root"] = root

	env := commandHelp["environment"]
	env.options = append(env.options, "--claude-version V Exact Claude Code npm version (default latest; none skips).")
	commandHelp["environment"] = env
}

func addHelpExamples(key string, examples ...string) {
	entry := commandHelp[key]
	entry.examples = append(entry.examples, examples...)
	commandHelp[key] = entry
}
