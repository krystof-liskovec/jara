package jara

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"sort"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

func remoteCommand(c *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("use remote add, list, or use")
	}
	switch args[0] {
	case "list":
		names := make([]string, 0, len(c.Remotes))
		for n := range c.Remotes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			mark := " "
			if n == c.Default {
				mark = "*"
			}
			fmt.Printf("%s %s\t%s\n", mark, n, c.Remotes[n].Host)
		}
		return nil
	case "use":
		if len(args) != 2 {
			return fmt.Errorf("use remote use NAME")
		}
		if _, ok := c.Remotes[args[1]]; !ok {
			return fmt.Errorf("unknown remote")
		}
		c.Default = args[1]
		return saveConfig(*c)
	case "remove":
		if len(args) != 2 || !nameRE.MatchString(args[1]) {
			return fmt.Errorf("use remote remove NAME")
		}
		name := args[1]
		if _, ok := c.Remotes[name]; !ok {
			return fmt.Errorf("unknown remote %q", name)
		}
		if _, managed := c.HostDeployments[name]; managed {
			return fmt.Errorf("%q was deployed by Jara; use jara deployment uninstall %s", name, name)
		}
		forgetRemote(c, name)
		return saveConfig(*c)
	case "add":
		if len(args) < 2 || !nameRE.MatchString(args[1]) {
			return fmt.Errorf("a name containing letters, digits, underscores or hyphens is required")
		}
		f := flag.NewFlagSet("remote add", flag.ContinueOnError)
		r := Remote{}
		f.StringVar(&r.Host, "host", "", "SSH host or alias")
		f.IntVar(&r.Port, "port", 0, "SSH port (default: SSH configuration)")
		f.StringVar(&r.User, "user", "", "SSH user")
		f.StringVar(&r.Identity, "identity", "", "private key path")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return fmt.Errorf("unexpected argument")
		}
		if _, managed := c.HostDeployments[args[1]]; managed {
			return fmt.Errorf("%q was deployed by Jara; use jara deployment uninstall %s", args[1], args[1])
		}
		if err := validateRemote(r); err != nil {
			return err
		}
		if err := doctor(args[1], r); err != nil {
			return err
		}
		c.Remotes[args[1]] = r
		if c.Default == "" {
			c.Default = args[1]
		}
		return saveConfig(*c)
	}
	return fmt.Errorf("unknown remote command")
}

func configCommand(c *Config, args []string) error {
	if len(args) > 0 && args[0] == "git-identity" {
		return gitIdentityConfigCommand(*c, args[1:])
	}
	if len(args) > 0 && args[0] == "codex-args" {
		return codexArgsConfigCommand(*c, args[1:])
	}
	if len(args) > 0 && args[0] == "claude-args" {
		return agentArgsConfigCommand(*c, protocol.AgentClaude, args[1:])
	}
	if len(args) >= 3 && args[0] == "set" && args[1] == "agent" {
		return agentConfigCommand(c, args[2:])
	}
	if len(args) == 4 && args[0] == "set" && args[1] == "editor-command" {
		if !nameRE.MatchString(args[2]) {
			return fmt.Errorf("invalid editor name")
		}
		var argv []string
		if err := json.Unmarshal([]byte(args[3]), &argv); err != nil || len(argv) == 0 || argv[0] == "" {
			return fmt.Errorf("editor-command requires a nonempty JSON array of strings")
		}
		if c.EditorCommands == nil {
			c.EditorCommands = map[string][]string{}
		}
		c.EditorCommands[args[2]] = argv
		return saveConfig(*c)
	}
	if len(args) < 3 || args[0] != "set" || args[1] != "editor" {
		return fmt.Errorf("use config set editor code|vim|nvim [--remote NAME] or config set agent codex|claude [--remote NAME | --workspace]")
	}
	e := args[2]
	if e != "code" && e != "vim" && e != "nvim" && len(c.EditorCommands[e]) == 0 {
		return fmt.Errorf("unsupported editor")
	}
	if len(args) == 3 {
		c.Editor = e
	} else if len(args) == 5 && args[3] == "--remote" {
		r, ok := c.Remotes[args[4]]
		if !ok {
			return fmt.Errorf("unknown remote")
		}
		r.Editor = e
		c.Remotes[args[4]] = r
	} else {
		return fmt.Errorf("invalid editor arguments")
	}
	return saveConfig(*c)
}

func codexArgsConfigCommand(c Config, args []string) error {
	return agentArgsConfigCommand(c, protocol.AgentCodex, args)
}

func agentArgsConfigCommand(c Config, runtime string, args []string) error {
	agent := protocol.AgentDisplayName(runtime)
	usage := fmt.Errorf("use jara config %s-args show|clear [--remote NAME] or set [--remote NAME] -- ARGS...", runtime)
	if len(args) == 0 {
		return usage
	}
	action := args[0]
	flags := args[1:]
	var values []string
	if action == "set" {
		flags, values = splitCodexArgs(flags)
	}
	set := flag.NewFlagSet("config "+runtime+"-args "+action, flag.ContinueOnError)
	remoteName := set.String("remote", c.Default, "named remote")
	if err := set.Parse(flags); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q; put %s arguments after --", set.Arg(0), agent)
	}
	name, remote, err := resolveConfigRemote(c, *remoteName)
	if err != nil {
		return err
	}
	switch action {
	case "show":
		var current []string
		if err = rpc(remote, Request{Op: runtime + "-args-show"}, nil, &current); err != nil {
			return fmt.Errorf("read %s arguments from %s: %w", agent, name, err)
		}
		fmt.Printf("Effective %s arguments on %s:\n", agent, name)
		if len(current) == 0 {
			fmt.Println("  (none)")
			return nil
		}
		for i, arg := range current {
			fmt.Printf("  %d. %q\n", i+1, arg)
		}
		return nil
	case "set":
		if err = validateCodexArgs(values); err != nil {
			return err
		}
		raw, _ := json.Marshal(values)
		if err = rpc(remote, Request{Op: runtime + "-args-set"}, bytes.NewReader(raw), nil); err != nil {
			return fmt.Errorf("set %s arguments on %s; update Jara on that host if needed: %w", agent, name, err)
		}
		fmt.Printf("Saved %d default %s argument(s) on %s. They apply to new %s processes.\n", len(values), agent, name, agent)
		return nil
	case "clear":
		if err = rpc(remote, Request{Op: runtime + "-args-clear"}, nil, nil); err != nil {
			return fmt.Errorf("clear %s arguments on %s; update Jara on that host if needed: %w", agent, name, err)
		}
		fmt.Printf("Cleared the saved %s arguments on %s; deployment defaults now apply.\n", agent, name)
		return nil
	default:
		return usage
	}
}

func resolveConfigRemote(c Config, name string) (string, Remote, error) {
	if name == "" && len(c.Remotes) == 1 {
		for only := range c.Remotes {
			name = only
		}
	}
	r, ok := c.Remotes[name]
	if !ok {
		return "", Remote{}, fmt.Errorf("select a configured host with --remote NAME or set a default with `jara remote use NAME`")
	}
	return name, r, nil
}

func setRemoteCodexArgs(remote Remote, args []string) error {
	if err := validateCodexArgs(args); err != nil {
		return err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return rpc(remote, Request{Op: "codex-args-set"}, bytes.NewReader(raw), nil)
}

func clearRemoteCodexArgs(remote Remote) error {
	return rpc(remote, Request{Op: "codex-args-clear"}, nil, nil)
}
