package jara

import (
	"errors"
	"fmt"
	"io"
	"sort"
)

func clientMenu(c Config) error {
	for {
		items := clientMenuItems(c)
		selected, err := selectMenu("Client menu", items)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if items[selected].Action == "help" {
			printHelpEntry("root", commandHelp["root"])
			return nil
		}
		if err = runClientMenuAction(c, items[selected].Action); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		c, err = readConfig()
		if err != nil {
			return err
		}
	}
}

func clientMenuItems(c Config) []menuItem {
	items := []menuItem{}
	if len(c.Remotes) == 0 {
		items = append(items, menuItem{Title: "Set up or connect a remote", Detail: "Guided setup for a Jara server", Kind: "new", Action: "setup"})
	} else {
		remote := defaultRemoteLabel(c)
		items = append(items,
			menuItem{Title: "Open this workspace", Detail: "Attach or choose a session · " + remote, Kind: "workspace", Action: "open"},
			menuItem{Title: "Projects", Detail: "Conversations and live terminals", Kind: "running", Action: "browse"},
			menuItem{Title: "New conversation", Detail: "Codex", Kind: "new", Action: "new"},
			menuItem{Title: "Connections", Detail: fmt.Sprintf("%d configured", len(c.Remotes)), Kind: "remote", Action: "remote"},
			menuItem{Title: "Credentials", Detail: "Codex and Git", Kind: "tools", Action: "auth"},
			menuItem{Title: "Diagnostics", Detail: "Host health", Kind: "tools", Action: "doctor"},
		)
	}
	if len(c.HostDeployments) > 0 {
		items = append(items, menuItem{Title: "Deployment", Detail: fmt.Sprintf("%d guided deployment(s)", len(c.HostDeployments)), Kind: "remote", Action: "deployment"})
	}
	items = append(items, menuItem{Title: "Help", Detail: "Commands and shortcuts", Kind: "tools", Action: "help"})
	return items
}

func runClientMenuAction(c Config, action string) error {
	switch action {
	case "open":
		return Run(nil)
	case "browse":
		name, err := chooseMenuRemote(c, "Browse remote workspaces")
		if err != nil || name == "" {
			return err
		}
		return Run([]string{"sessions", "--remote", name})
	case "new":
		name, err := chooseMenuRemote(c, "Start a new session")
		if err != nil || name == "" {
			return err
		}
		return Run([]string{"new", "--remote", name})
	case "setup":
		return guidedSetup(c)
	case "remote":
		return remoteConnectionsMenu(c)
	case "auth":
		return authenticationMenu(c)
	case "doctor":
		name, err := chooseMenuRemote(c, "Check remote setup")
		if err != nil || name == "" {
			return err
		}
		return doctor(name, c.Remotes[name])
	case "deployment":
		return deploymentMenu(c)
	default:
		return fmt.Errorf("unknown client menu action %q", action)
	}
}

func remoteConnectionsMenu(c Config) error {
	items := []menuItem{}
	if len(c.Remotes) > 0 {
		items = append(items, menuItem{Title: "Choose default remote", Detail: defaultRemoteLabel(c), Kind: "remote", Action: "use"})
	}
	items = append(items, menuItem{Title: "Add or set up a remote", Detail: "Connect to an existing server or deploy Jara", Kind: "new", Action: "setup"})
	if hasRemovableRemote(c) {
		items = append(items, menuItem{Title: "Remove a remote", Detail: "Forget a manually configured connection", Kind: "remote", Action: "remove"})
	}
	selected, err := selectMenu("Remote connections", items)
	if err != nil {
		return err
	}
	switch items[selected].Action {
	case "use":
		name, err := chooseMenuRemote(c, "Choose default remote")
		if err != nil || name == "" {
			return err
		}
		return remoteCommand(&c, []string{"use", name})
	case "setup":
		return guidedSetup(c)
	case "remove":
		name, err := chooseRemovableRemote(c)
		if err != nil || name == "" {
			return err
		}
		return remoteCommand(&c, []string{"remove", name})
	default:
		return fmt.Errorf("unknown remote menu action")
	}
}

func authenticationMenu(c Config) error {
	items := []menuItem{
		{Title: "Sign in to Codex", Detail: "Use device authorization on a remote", Kind: "tools", Action: "codex"},
		{Title: "Configure Git credentials", Detail: "Share credentials across repositories on one Git server", Kind: "tools", Action: "git"},
	}
	selected, err := selectMenu("Sign-in and Git access", items)
	if err != nil {
		return err
	}
	name, err := chooseMenuRemote(c, "Choose a remote")
	if err != nil || name == "" {
		return err
	}
	provider := items[selected].Action
	return authCommand(c, []string{provider, "--remote", name})
}

func deploymentMenu(c Config) error {
	items := []menuItem{
		{Title: "Upgrade Jara", Detail: "Deploy the server code embedded in this CLI", Kind: "remote", Action: "upgrade"},
		{Title: "Uninstall Jara", Detail: "Stop the deployment and choose what data to remove", Kind: "remote", Action: "uninstall"},
	}
	selected, err := selectMenu("Manage deployment", items)
	if err != nil {
		return err
	}
	name, err := chooseMenuDeployment(c)
	if err != nil || name == "" {
		return err
	}
	action := items[selected].Action
	return deploymentCommand(&c, []string{action, name})
}

func chooseMenuRemote(c Config, title string) (string, error) {
	if len(c.Remotes) == 0 {
		return "", fmt.Errorf("no remote is configured; run jara setup")
	}
	names := make([]string, 0, len(c.Remotes))
	for name := range c.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 1 {
		return names[0], nil
	}
	items := make([]menuItem, 0, len(names))
	for _, name := range names {
		detail := c.Remotes[name].Host
		if name == c.Default {
			detail += " · default"
		}
		items = append(items, menuItem{Title: name, Detail: detail, Kind: "remote"})
	}
	selected, err := selectMenu(title, items)
	if err != nil {
		return "", err
	}
	return items[selected].Title, nil
}

func chooseMenuDeployment(c Config) (string, error) {
	if len(c.HostDeployments) == 0 {
		return "", fmt.Errorf("no guided deployment is configured")
	}
	names := make([]string, 0, len(c.HostDeployments))
	for name := range c.HostDeployments {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 1 {
		return names[0], nil
	}
	items := make([]menuItem, 0, len(names))
	for _, name := range names {
		items = append(items, menuItem{Title: name, Detail: c.HostDeployments[name].Admin.Host, Kind: "deployment"})
	}
	selected, err := selectMenu("Choose a deployment", items)
	if err != nil {
		return "", err
	}
	return items[selected].Title, nil
}

func chooseRemovableRemote(c Config) (string, error) {
	names := make([]string, 0, len(c.Remotes))
	for name := range c.Remotes {
		if _, managed := c.HostDeployments[name]; !managed {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", fmt.Errorf("no manually configured remote can be removed")
	}
	if len(names) == 1 {
		return names[0], nil
	}
	items := make([]menuItem, 0, len(names))
	for _, name := range names {
		items = append(items, menuItem{Title: name, Detail: c.Remotes[name].Host, Kind: "remote"})
	}
	selected, err := selectMenu("Remove a remote", items)
	if err != nil {
		return "", err
	}
	return items[selected].Title, nil
}

func hasRemovableRemote(c Config) bool {
	for name := range c.Remotes {
		if _, managed := c.HostDeployments[name]; !managed {
			return true
		}
	}
	return false
}

func defaultRemoteLabel(c Config) string {
	if remote, ok := c.Remotes[c.Default]; ok {
		return c.Default + " · " + remote.Host
	}
	if len(c.Remotes) == 1 {
		for name, remote := range c.Remotes {
			return name + " · " + remote.Host
		}
	}
	return "choose a remote"
}
