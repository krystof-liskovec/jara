package jara

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"
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
		if err := validateRemote(r); err != nil {
			return err
		}
		if err := doctor(r); err != nil {
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
		return fmt.Errorf("use config set editor code|vim|nvim [--remote NAME]")
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
