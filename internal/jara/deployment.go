package jara

import "fmt"

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
