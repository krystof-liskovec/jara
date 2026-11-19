package jara

import (
	"fmt"
	"os"
)

func deploymentCommand(c *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("use jara deployment upgrade [NAME] or jara deployment uninstall [NAME] [--volumes]")
	}
	switch args[0] {
	case "upgrade":
		return deploymentUpgradeCommand(*c, args[1:])
	case "uninstall":
		return uninstallCommand(c, args[1:])
	default:
		return fmt.Errorf("unknown deployment command %q; see jara --help", args[0])
	}
}

type deploymentUpgradeOptions struct {
	force             bool
	forceDowngrade    bool
	keepCodexVersion  bool
	keepClaudeVersion bool
	assumeYes         bool
}

func deploymentUpgradeCommand(c Config, args []string) error {
	name := ""
	verbose := false
	var options deploymentUpgradeOptions
	for _, arg := range args {
		switch arg {
		case "--force":
			options.force = true
		case "--force-downgrade":
			options.forceDowngrade = true
		case "--keep-codex-version":
			options.keepCodexVersion = true
		case "--keep-claude-version":
			options.keepClaudeVersion = true
		case "-y":
			options.assumeYes = true
		case "-v", "--verbose":
			verbose = true
		default:
			if name != "" {
				return fmt.Errorf("use jara deployment upgrade [NAME] [-y] [-v] [--force] [--force-downgrade] [--keep-codex-version] [--keep-claude-version]")
			}
			name = arg
		}
	}
	if len(deploymentBundle()) == 0 {
		return fmt.Errorf("this Jara CLI does not include the source bundle; reinstall it with install.sh")
	}
	if name == "" {
		if _, ok := c.HostDeployments[c.Default]; ok {
			name = c.Default
		} else if len(c.HostDeployments) == 1 {
			for candidate := range c.HostDeployments {
				name = candidate
			}
		} else {
			name = c.Default
		}
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("choose a guided deployment with `jara deployment upgrade NAME`")
	}
	deployment, ok := c.HostDeployments[name]
	if !ok {
		if _, connected := c.Remotes[name]; !connected {
			return fmt.Errorf("unknown remote %q; run jara setup to connect it", name)
		}
		return fmt.Errorf("%q is connected, but this computer has no saved Docker host connection for upgrades; run jara setup, enter remote name %q, and enable host management", name, name)
	}
	if !validGuidedDeploymentDirectory(deploymentName(name, deployment), deployment.Directory) {
		return fmt.Errorf("saved deployment directory is invalid; refusing to upgrade it")
	}
	if err := validateRemote(deployment.Admin); err != nil {
		return fmt.Errorf("invalid saved host connection: %w", err)
	}
	if verbose {
		return upgradeGuidedHost(name, deployment, deploymentBundle(), Version, options, verboseUpgrade{out: os.Stdout})
	}
	board := newUpgradeBoard(os.Stdout)
	row := board.row(name)
	board.start()
	err := upgradeGuidedHost(name, deployment, deploymentBundle(), Version, options, row)
	row.settle(err)
	board.close()
	return err
}
