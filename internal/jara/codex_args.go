package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	var args []string
	if len(raw) > 65536 || json.Unmarshal(raw, &args) != nil || args == nil {
		return nil, fmt.Errorf("JARA_CODEX_ARGS must be a JSON array of strings, for example [\"--model\",\"gpt-6.1-sol\"]")
	}
	return args, validateCodexArgs(args)
}

func configuredCodexArgs(extra []string) ([]string, error) {
	raw, present := os.LookupEnv("JARA_CODEX_ARGS")
	if !present {
		data, err := os.ReadFile(codexArgsFile)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			raw = string(data)
		} else {
			raw = "[]"
		}
	}
	args, err := parseCodexArgs([]byte(raw))
	if err != nil {
		return nil, err
	}
	override, err := os.ReadFile(filepath.Join(dataRoot(), codexArgsOverrideFile))
	if err == nil {
		args, err = parseCodexArgs(override)
		if err != nil {
			return nil, fmt.Errorf("read saved Codex arguments: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read saved Codex arguments: %w", err)
	}
	args = append(args, extra...)
	return args, validateCodexArgs(args)
}

func saveCodexArgsOverride(args []string) error {
	if err := validateCodexArgs(args); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dataRoot(), codexArgsOverrideFile), args)
}

func clearCodexArgsOverride() error {
	err := os.Remove(filepath.Join(dataRoot(), codexArgsOverrideFile))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func codexCommand(resume bool, args []string) []string {
	command := []string{"/usr/local/bin/codex"}
	if resume {
		command = append(command, "resume")
	}
	return append(command, args...)
}

func warnRunningCodexArgs(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "Attaching to an existing session; Codex arguments apply only to new processes. Use jara new to start another session with these arguments.")
	}
}
