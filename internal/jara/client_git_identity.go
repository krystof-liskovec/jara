package jara

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
)

func gitIdentityConfigCommand(c Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("use jara config git-identity show|set|clear [OPTIONS]")
	}
	action := args[0]
	f := flag.NewFlagSet("config git-identity "+action, flag.ContinueOnError)
	remoteName := f.String("remote", c.Default, "named remote")
	local := f.Bool("local", false, "configure this environment instead of a remote")
	name := f.String("name", "", "Git author name")
	email := f.String("email", "", "Git author email")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	remoteSet, nameSet, emailSet := false, false, false
	f.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "remote":
			remoteSet = true
		case "name":
			nameSet = true
		case "email":
			emailSet = true
		}
	})
	if *local && remoteSet {
		return fmt.Errorf("--local and --remote cannot be used together")
	}
	if action != "set" && (nameSet || emailSet) {
		return fmt.Errorf("--name and --email are only valid with set")
	}
	if action == "set" {
		if !nameSet || !emailSet || strings.TrimSpace(*name) == "" || strings.TrimSpace(*email) == "" {
			return fmt.Errorf("set requires both nonempty --name and --email")
		}
	}
	if action != "show" && action != "set" && action != "clear" {
		return fmt.Errorf("use jara config git-identity show|set|clear [OPTIONS]")
	}

	if *local {
		return localGitIdentityCommand(action, gitIdentity{Name: *name, Email: *email})
	}
	if remoteSet && strings.TrimSpace(*remoteName) == "" {
		return fmt.Errorf("--remote requires a remote name")
	}
	remote, r, err := resolveConfigRemote(c, *remoteName)
	if err != nil {
		return err
	}
	return remoteGitIdentityCommand(action, remote, r, gitIdentity{Name: *name, Email: *email})
}

func localGitIdentityCommand(action string, identity gitIdentity) error {
	switch action {
	case "show":
		current, err := readGitIdentity()
		if err != nil {
			return fmt.Errorf("read local Git identity: %w", err)
		}
		printGitIdentity("local environment", current)
	case "set":
		if err := setGitIdentity(identity); err != nil {
			return fmt.Errorf("set local Git identity: %w", err)
		}
		fmt.Println("Saved Git commit identity for this environment.")
	case "clear":
		if err := clearGitIdentity(); err != nil {
			return fmt.Errorf("clear local Git identity: %w", err)
		}
		fmt.Println("Cleared the managed Git commit identity for this environment.")
	}
	return nil
}

func remoteGitIdentityCommand(action, remoteName string, remote Remote, identity gitIdentity) error {
	switch action {
	case "show":
		current, err := readRemoteGitIdentity(remote)
		if err != nil {
			return fmt.Errorf("read Git identity from %s: %w", remoteName, err)
		}
		printGitIdentity("remote "+remoteName, current)
	case "set":
		if err := setRemoteGitIdentity(remote, identity); err != nil {
			return fmt.Errorf("set Git identity on %s: %w", remoteName, err)
		}
		fmt.Printf("Saved Git commit identity for remote %s.\n", remoteName)
	case "clear":
		if err := clearRemoteGitIdentity(remote); err != nil {
			return fmt.Errorf("clear Git identity on %s: %w", remoteName, err)
		}
		fmt.Printf("Cleared the managed Git commit identity for remote %s.\n", remoteName)
	}
	return nil
}

func readRemoteGitIdentity(remote Remote) (gitIdentity, error) {
	var identity gitIdentity
	err := rpc(remote, Request{Op: "git-identity-show"}, nil, &identity)
	return identity, err
}

func setRemoteGitIdentity(remote Remote, identity gitIdentity) error {
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return rpc(remote, Request{Op: "git-identity-set"}, bytes.NewReader(raw), nil)
}

func clearRemoteGitIdentity(remote Remote) error {
	return rpc(remote, Request{Op: "git-identity-clear"}, nil, nil)
}

func printGitIdentity(scope string, identity gitIdentity) {
	name := identity.Name
	email := identity.Email
	if name == "" {
		name = "(not set)"
	}
	if email == "" {
		email = "(not set)"
	}
	fmt.Printf("Git commit identity (%s):\nName: %s\nEmail: %s\n", chatText(scope), chatText(name), chatText(email))
}
