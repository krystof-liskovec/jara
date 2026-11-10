package jara

import (
	"fmt"
	"os"
	"path/filepath"
)

// validateAgentFlags checks --agent, --interface and --note before any remote
// work starts, so a bad combination fails without side effects.
func validateAgentFlags(action, agent, userInterface, note string) error {
	if note != "" && action != "handoff" {
		return fmt.Errorf("--note requires jara handoff")
	}
	if len(note) > maxBackgroundPrompt {
		return fmt.Errorf("handoff note exceeds 64 KiB")
	}
	if action == "handoff" && userInterface == "jara" {
		return fmt.Errorf("jara handoff starts an agent terminal; omit --interface")
	}
	if agent == "" {
		return nil
	}
	runtime, err := normalizeAgentRuntime(agent)
	if err != nil {
		return err
	}
	return validateAgentInterface(runtime, userInterface)
}

// resolveClientAgent picks the runtime for a session: an explicit --agent,
// then the folder's workspace default, the remote default, and the global
// default. A handoff without --agent stays empty so the host can choose the
// agent that did not run most recently in the workspace.
func resolveClientAgent(c Config, root, remote, action, explicit string) (string, error) {
	if explicit != "" {
		return normalizeAgentRuntime(explicit)
	}
	if action == "handoff" {
		return "", nil
	}
	for _, candidate := range []string{c.WorkspaceAgents[root], c.Remotes[remote].Agent, c.Agent} {
		if candidate != "" {
			return normalizeAgentRuntime(candidate)
		}
	}
	return agentCodex, nil
}

// agentConfigCommand implements `jara config set agent NAME [--remote NAME |
// --workspace]`; "default" removes a remote or workspace override.
func agentConfigCommand(c *Config, args []string) error {
	usage := fmt.Errorf("use jara config set agent codex|claude|default [--remote NAME | --workspace]")
	if len(args) != 1 && len(args) != 2 && len(args) != 3 {
		return usage
	}
	value := args[0]
	runtime := ""
	if value != "default" {
		var err error
		if runtime, err = normalizeAgentRuntime(value); err != nil {
			return err
		}
	}
	switch {
	case len(args) == 1:
		c.Agent = runtime
	case len(args) == 3 && args[1] == "--remote":
		r, ok := c.Remotes[args[2]]
		if !ok {
			return fmt.Errorf("unknown remote %q", args[2])
		}
		r.Agent = runtime
		c.Remotes[args[2]] = r
	case len(args) == 2 && args[1] == "--workspace":
		root, err := currentWorkspaceRoot(*c)
		if err != nil {
			return err
		}
		if c.WorkspaceAgents == nil {
			c.WorkspaceAgents = map[string]string{}
		}
		if runtime == "" {
			delete(c.WorkspaceAgents, root)
		} else {
			c.WorkspaceAgents[root] = runtime
		}
	default:
		return usage
	}
	return saveConfig(*c)
}

// currentWorkspaceRoot matches the root the client uses to look up bindings.
func currentWorkspaceRoot(c Config) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if resolved, e := filepath.EvalSymlinks(cwd); e == nil {
		cwd = resolved
	}
	root, _, _ := workspaceContext(c, cwd, false)
	if resolved, e := filepath.EvalSymlinks(root); e == nil {
		root = resolved
	}
	return root, nil
}
