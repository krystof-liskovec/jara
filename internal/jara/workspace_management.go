package jara

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"krystof.liskovec/jara/internal/textutil"
)

func workspaceCommand(c *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("use jara workspace list or jara workspace delete ID")
	}
	switch args[0] {
	case "list":
		return listWorkspacesCommand(*c, args[1:])
	case "delete", "remove", "clear":
		return deleteWorkspaceCommand(c, args[1:])
	default:
		return fmt.Errorf("unknown workspace command %q", args[0])
	}
}

func listWorkspacesCommand(c Config, args []string) error {
	remoteName, positional, err := workspaceCommandArgs(args, c.Default)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return fmt.Errorf("use jara workspace list [--remote NAME]")
	}
	remoteName, remote, err := resolveConfigRemote(c, remoteName)
	if err != nil {
		return err
	}
	var workspaces []Workspace
	if err = rpc(remote, Request{Op: "workspace-list"}, nil, &workspaces); err != nil {
		return fmt.Errorf("list workspaces on %s: %w", remoteName, err)
	}
	if len(workspaces) == 0 {
		fmt.Printf("No workspaces found on %s.\n", remoteName)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATE\tORIGIN")
	for _, workspace := range workspaces {
		name := workspaceTitle(workspace)
		state := "unfinished"
		if workspace.Ready {
			state = "ready"
		}
		origin := workspace.URL
		if origin == "" {
			origin = "(copied)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", workspace.ID, textutil.SanitizeText(name), state, textutil.SanitizeText(origin))
	}
	return w.Flush()
}

func deleteWorkspaceCommand(c *Config, args []string) error {
	remoteName, positional, err := workspaceCommandArgs(args, c.Default)
	if err != nil {
		return err
	}
	if len(positional) != 1 || !nameRE.MatchString(positional[0]) {
		return fmt.Errorf("use jara workspace delete ID [--remote NAME]")
	}
	id := positional[0]
	remoteName, remote, err := resolveConfigRemote(*c, remoteName)
	if err != nil {
		return err
	}
	var workspace Workspace
	if err = rpc(remote, Request{Op: "info", ID: id}, nil, &workspace); err != nil {
		return fmt.Errorf("find workspace %s on %s: %w", id, remoteName, err)
	}
	name := workspaceTitle(workspace)
	fmt.Printf("This will permanently delete workspace %s (%s) on %s, including its Jara-managed Git worktrees.\n", textutil.SanitizeText(name), id, remoteName)
	if workspace.URL != "" {
		fmt.Printf("Origin: %s\n", textutil.SanitizeText(workspace.URL))
	}
	confirmed, err := confirm("Delete this workspace?")
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	if err = rpc(remote, Request{Op: "workspace-delete", ID: id}, nil, nil); err != nil {
		return fmt.Errorf("delete workspace %s on %s: %w", id, remoteName, err)
	}
	if forgetLocalWorkspace(c, remoteName, id) {
		if err = saveConfig(*c); err != nil {
			return fmt.Errorf("workspace was deleted remotely, but local Jara links could not be cleared: %w", err)
		}
	}
	fmt.Printf("Deleted workspace %s from %s.\n", id, remoteName)
	return nil
}

func workspaceCommandArgs(args []string, defaultRemote string) (string, []string, error) {
	remoteName := defaultRemote
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--remote" {
			i++
			if i == len(args) || args[i] == "" {
				return "", nil, fmt.Errorf("--remote requires a name")
			}
			remoteName = args[i]
		} else if strings.HasPrefix(arg, "--remote=") {
			remoteName = strings.TrimPrefix(arg, "--remote=")
			if remoteName == "" {
				return "", nil, fmt.Errorf("--remote requires a name")
			}
		} else if strings.HasPrefix(arg, "-") {
			return "", nil, fmt.Errorf("unknown workspace option %q", arg)
		} else {
			positional = append(positional, arg)
		}
	}
	return remoteName, positional, nil
}

func forgetLocalWorkspace(c *Config, remoteName, id string) bool {
	changed := false
	for root, binding := range c.Bindings {
		if binding.Remote == remoteName && binding.Workspace == id {
			delete(c.Bindings, root)
			changed = true
		}
	}
	for key, binding := range c.Associations {
		if binding.Remote == remoteName && binding.Workspace == id {
			delete(c.Associations, key)
			changed = true
		}
	}
	return changed
}
