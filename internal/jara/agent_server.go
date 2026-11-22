package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/protocol"
)

// The agent router runs as `jara _agent-server` between the Jara interface
// and the Codex app-server; see internal/agentrouter.

// agentServerCommand wraps the Codex app-server command in the router.
// An explicitly chosen agent (--agent or the browser's "New ... conversation")
// opens the session's first conversation even when a default model names the
// other agent; later new conversations follow the default model.
func agentServerCommand(binary string, args []string, defaultAgent string, explicit bool, claudeArgs []string) (string, []string) {
	self, err := os.Executable()
	if err != nil {
		self = jaraBinary
	}
	if defaultAgent == "" {
		defaultAgent = protocol.AgentCodex
	}
	command := []string{"_agent-server", "--default-agent", defaultAgent}
	if explicit {
		command = append(command, "--agent-explicit", "true")
	}
	if len(claudeArgs) > 0 {
		encoded, _ := json.Marshal(claudeArgs)
		command = append(command, "--claude-args", string(encoded))
	}
	return self, append(append(command, "--", binary), args...)
}

func agentServerMain(args []string) error {
	codexCommand, options, err := agentServerOptions(args)
	if err != nil {
		return err
	}
	return agentrouter.Run(os.Stdin, os.Stdout, codexCommand, options)
}

// agentServerOptions reads the _agent-server arguments agentServerCommand
// writes, adding this host's default Claude Code arguments.
func agentServerOptions(args []string) ([]string, agentrouter.Options, error) {
	defaultAgent, explicit := protocol.AgentCodex, false
	var claudeArgs []string
	for len(args) >= 2 && strings.HasPrefix(args[0], "--") && args[0] != "--" {
		switch args[0] {
		case "--default-agent":
			defaultAgent = args[1]
		case "--agent-explicit":
			explicit = args[1] == "true"
		case "--claude-args":
			parsed, err := parseAgentArgs(protocol.AgentClaude, []byte(args[1]))
			if err != nil {
				return nil, agentrouter.Options{}, err
			}
			claudeArgs = parsed
		default:
			return nil, agentrouter.Options{}, fmt.Errorf("unknown agent server option %s", args[0])
		}
		args = args[2:]
	}
	if len(args) < 2 || args[0] != "--" {
		return nil, agentrouter.Options{}, fmt.Errorf("use jara _agent-server [--default-agent NAME] -- CODEX_APP_SERVER_COMMAND")
	}
	runtime, err := protocol.NormalizeAgent(defaultAgent)
	if err != nil {
		return nil, agentrouter.Options{}, err
	}
	// Host defaults for Claude Code come from this host's Jara configuration.
	configured, err := configuredAgentArgs(protocol.AgentClaude, claudeArgs)
	if err != nil {
		return nil, agentrouter.Options{}, err
	}
	return args[1:], agentrouter.Options{DefaultAgent: runtime, AgentExplicit: explicit, ClaudeArgs: configured}, nil
}
