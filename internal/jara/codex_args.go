package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

const codexArgsFile = "/run/jara/codex-args.json"
const codexArgsOverrideFile = "codex-args.json"

type codexArgList []string

func (a *codexArgList) String() string         { return strings.Join(*a, " ") }
func (a *codexArgList) Set(value string) error { *a = append(*a, value); return nil }

func splitCodexArgs(args []string) ([]string, []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func validateCodexArgs(args []string) error {
	if len(args) > 128 {
		return fmt.Errorf("at most 128 additional Codex arguments are allowed")
	}
	size := 0
	for _, arg := range args {
		size += len(arg)
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("Codex arguments cannot contain NUL bytes")
		}
	}
	if size > 65536 {
		return fmt.Errorf("additional Codex arguments exceed 64 KiB")
	}
	return nil
}

func parseCodexArgs(raw []byte) ([]string, error) {
	return parseAgentArgs(protocol.AgentCodex, raw)
}

func parseAgentArgs(runtime string, raw []byte) ([]string, error) {
	var args []string
	if len(raw) > 65536 || json.Unmarshal(raw, &args) != nil || args == nil {
		return nil, fmt.Errorf("%s must be a JSON array of strings, for example [\"--model\",\"%s\"]", agentArgsEnv(runtime), exampleModel(runtime))
	}
	return args, validateCodexArgs(args)
}

func exampleModel(runtime string) string {
	if runtime == protocol.AgentClaude {
		return "opus"
	}
	return "gpt-6.1-sol"
}

// agentArgsEnv names the deployment variable holding default arguments; the
// entrypoint copies it to agentArgsFile because SSH sessions do not inherit it.
func agentArgsEnv(runtime string) string {
	if runtime == protocol.AgentClaude {
		return "JARA_CLAUDE_ARGS"
	}
	return "JARA_CODEX_ARGS"
}

func agentArgsFile(runtime string) string {
	if runtime == protocol.AgentClaude {
		return "/run/jara/claude-args.json"
	}
	return codexArgsFile
}

func agentArgsOverrideFile(runtime string) string {
	if runtime == protocol.AgentClaude {
		return filepath.Join(fsutil.DataRoot(), "claude-args.json")
	}
	return filepath.Join(fsutil.DataRoot(), codexArgsOverrideFile)
}

func configuredCodexArgs(extra []string) ([]string, error) {
	return configuredAgentArgs(protocol.AgentCodex, extra)
}

// configuredAgentArgs returns the arguments an agent starts with: Jara's MCP
// servers, then the saved arguments. Later Codex overrides win, so saved
// arguments can still change a server.
func configuredAgentArgs(runtime string, extra []string) ([]string, error) {
	args, err := savedAgentArgs(runtime, extra)
	if err != nil {
		return nil, err
	}
	servers, err := mcpLaunchArgs(runtime)
	if err != nil {
		return nil, fmt.Errorf("MCP servers: %w", err)
	}
	return append(servers, args...), nil
}

// savedAgentArgs returns deployment defaults, replaced by a saved host
// override when present, followed by per-invocation arguments.
func savedAgentArgs(runtime string, extra []string) ([]string, error) {
	raw, present := os.LookupEnv(agentArgsEnv(runtime))
	if !present {
		data, err := os.ReadFile(agentArgsFile(runtime))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			raw = string(data)
		} else {
			raw = "[]"
		}
	}
	args, err := parseAgentArgs(runtime, []byte(raw))
	if err != nil {
		return nil, err
	}
	override, err := os.ReadFile(agentArgsOverrideFile(runtime))
	if err == nil {
		args, err = parseAgentArgs(runtime, override)
		if err != nil {
			return nil, fmt.Errorf("read saved %s arguments: %w", protocol.AgentDisplayName(runtime), err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read saved %s arguments: %w", protocol.AgentDisplayName(runtime), err)
	}
	args = append(args, extra...)
	return args, validateCodexArgs(args)
}

func saveAgentArgsOverride(runtime string, args []string) error {
	if err := validateCodexArgs(args); err != nil {
		return err
	}
	return fsutil.WriteJSON(agentArgsOverrideFile(runtime), args)
}

func clearAgentArgsOverride(runtime string) error {
	err := os.Remove(agentArgsOverrideFile(runtime))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// agentArgsRPC serves codex-args-* and claude-args-* host operations.
func agentArgsRPC(runtime, action string, input io.Reader, output io.Writer) error {
	switch action {
	case "show":
		args, err := savedAgentArgs(runtime, nil)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(args)
	case "set":
		raw, err := io.ReadAll(io.LimitReader(input, 65537))
		if err != nil {
			return err
		}
		args, err := parseAgentArgs(runtime, raw)
		if err != nil {
			return err
		}
		return saveAgentArgsOverride(runtime, args)
	case "clear":
		return clearAgentArgsOverride(runtime)
	}
	return fmt.Errorf("unknown agent argument operation")
}

func codexCommand(resume bool, args []string) []string {
	command := []string{"/usr/local/bin/codex"}
	if resume {
		command = append(command, "resume")
	}
	return append(command, args...)
}

func codexResumeLatestCommand(args []string) []string {
	return append([]string{"/usr/local/bin/codex", "resume", "--last", "--include-non-interactive"}, args...)
}

func codexResumeAllCommand(args []string) []string {
	return append([]string{"/usr/local/bin/codex", "resume", "--all", "--include-non-interactive"}, args...)
}

func warnRunningCodexArgs(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "Attaching to an existing session; Codex arguments apply only to new processes. Use jara new to start another session with these arguments.")
	}
}
