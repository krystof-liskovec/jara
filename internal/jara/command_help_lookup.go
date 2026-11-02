package jara

import (
	"fmt"
	"os"
	"strings"
)

var boolHelpOptions = map[string]bool{
	"clone": true, "copy": true, "worktree": true, "takeover": true,
	"force": true, "include-ignored": true, "delete": true, "resume": true,
	"new": true, "check": true, "native": true, "vim": true, "nvim": true,
	"docker": true, "token-stdin": true, "volumes": true,
}

func showCommandHelp(args []string) bool {
	if !hasHelpFlag(args) {
		return false
	}
	key := commandHelpKey(args)
	entry, ok := commandHelp[key]
	if !ok {
		key, entry = "root", commandHelp["root"]
	}
	printHelpEntry(key, entry)
	return true
}

func hasHelpFlag(args []string) bool {
	skipValue := false
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if skipValue {
			skipValue = false
			continue
		}
		if arg == "-h" || arg == "--help" {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") {
			name := strings.TrimLeft(arg, "-")
			if !boolHelpOptions[name] {
				skipValue = true
			}
		}
	}
	return false
}

func commandHelpKey(args []string) string {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "root"
	}
	command := args[0]
	switch command {
	case "help", "--help", "-h":
		return "root"
	case "open", "resume", "new", "connect", "sessions", "edit", "doctor", "forward", "reverse", "tunnel", "setup", "menu", "ui", "keygen", "environment", "migrate", "version":
		return command
	case "sync", "tools":
		return command
	case "key":
		if len(args) > 1 {
			key := command + " " + args[1]
			if _, ok := commandHelp[key]; ok {
				return key
			}
		}
		return command
	case "remote", "auth", "deployment", "workspace":
		if len(args) > 1 {
			key := command + " " + args[1]
			if _, ok := commandHelp[key]; ok {
				return key
			}
		}
		return command
	case "config":
		if len(args) > 1 {
			key := "config " + args[1]
			if args[1] == "set" && len(args) > 2 {
				key += " " + args[2]
			} else if args[1] == "codex-args" && len(args) > 2 {
				key += " " + args[2]
			}
			if _, ok := commandHelp[key]; ok {
				return key
			}
		}
		return "config"
	case "uninstall":
		return "uninstall"
	case "host":
		if len(args) > 1 {
			switch args[1] {
			case "update":
				return "host update"
			case "upgrade":
				return "host upgrade"
			}
		}
		return "host"
	case "editor":
		if len(args) > 1 && args[1] == "import" {
			return "editor import"
		}
		return "editor"
	default:
		return "root"
	}
}

func printHelpEntry(key string, entry helpEntry) {
	fmt.Fprintf(os.Stdout, "Usage:\n  %s\n\n%s\n", entry.usage, entry.summary)
	if len(entry.commands) > 0 {
		fmt.Fprintln(os.Stdout, "\nCommands:")
		for _, command := range entry.commands {
			fmt.Fprintf(os.Stdout, "  %s\n", command)
		}
	}
	fmt.Fprintln(os.Stdout, "\nOptions:")
	fmt.Fprintln(os.Stdout, "  -h, --help       Show usage and examples.")
	for _, option := range entry.options {
		fmt.Fprintf(os.Stdout, "  %s\n", option)
	}
	fmt.Fprintln(os.Stdout, "\nExamples:")
	for _, example := range entry.examples {
		fmt.Fprintf(os.Stdout, "  %s\n", example)
	}
	if key == "root" {
		fmt.Fprintln(os.Stdout, "\nRun `jara COMMAND -h` for command-specific usage and examples.")
	}
}
