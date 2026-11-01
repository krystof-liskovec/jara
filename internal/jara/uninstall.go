package jara

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

func uninstallCommand(c *Config, args []string) error {
	removeVolumes := false
	var positional []string
	for _, arg := range args {
		if arg == "--volumes" {
			removeVolumes = true
		} else if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("unknown uninstall option %q", arg)
		} else {
			positional = append(positional, arg)
		}
	}
	name := ""
	if len(positional) > 1 {
		return fmt.Errorf("use jara deployment uninstall [NAME] [--volumes]")
	}
	if len(positional) == 1 {
		name = positional[0]
	} else {
		name = c.Default
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("select a valid remote name")
	}
	deployment, ok := c.HostDeployments[name]
	if !ok {
		return fmt.Errorf("%q was not deployed by guided setup; use jara remote remove %s to forget its local connection", name, name)
	}
	if err := validateRemote(deployment.Admin); err != nil {
		return fmt.Errorf("invalid saved host connection: %w", err)
	}
	if !validGuidedDeploymentDirectory(name, deployment.Directory) {
		return fmt.Errorf("saved deployment directory is invalid; refusing to remove it")
	}
	generatedServiceKey := deployment.ServiceKeyMode == "" || deployment.ServiceKeyMode == "generated"
	if deployment.ServiceKeyMode != "" && deployment.ServiceKeyMode != "generated" && deployment.ServiceKeyMode != "existing" {
		return fmt.Errorf("saved service key mode is invalid; refusing to uninstall")
	}
	serviceKey := ""
	keyPairExists := false
	var err error
	if generatedServiceKey {
		serviceKey, err = jaraServiceKeyPath(name)
		if err != nil {
			return err
		}
		keyPairExists, err = serviceKeyPairExists(serviceKey)
		if err != nil {
			return err
		}
	}
	volumeMessage := "Workspace files, Codex login, Git credentials, history, and host keys in Docker volumes will be kept."
	volumeCommands := ""
	lowerName := strings.ToLower(name)
	if removeVolumes {
		volumeMessage = "This permanently deletes workspace files, Codex login, Git credentials, history, and host keys. A reinstall will have a new SSH host fingerprint."
		project := "jara-" + lowerName
		for _, volume := range []string{project + "-agent-home", project + "-host-keys"} {
			volumeCommands += "; if docker volume inspect " + quote(volume) + " >/dev/null 2>&1; then docker volume rm " + quote(volume) + " || exit; fi"
		}
	}
	fmt.Printf("This will stop Jara on %s and remove its deployment files. %s\n", deployment.Admin.Host, volumeMessage)
	if keyPairExists {
		fmt.Printf("The local Jara access key pair at %s will be kept unless you choose to remove it.\n", serviceKey)
	} else if !generatedServiceKey {
		fmt.Println("The existing SSH key you selected is user-owned and will not be removed.")
	}
	confirmed, err := askYesFromStdin("Continue?", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	removeLocalKey := false
	if keyPairExists {
		removeLocalKey, err = askYesFromStdin("Also remove the local Jara SSH key pair?", false)
		if err != nil {
			return err
		}
	}
	dir := `"$HOME/` + deployment.Directory + `"`
	container, image := "jara-agent-"+lowerName, "jara-agent-"+lowerName
	command := "if docker container inspect " + quote(container) + " >/dev/null 2>&1; then docker rm -f " + quote(container) + " || exit; fi"
	command += "; if docker image inspect " + quote(image) + " >/dev/null 2>&1; then docker image rm " + quote(image) + " || exit; fi"
	command += volumeCommands + "; rm -rf -- " + dir
	if err = runBootstrapSSH(deployment.Admin, command, nil); err != nil {
		return fmt.Errorf("remove Jara from host: %w", err)
	}
	forgetRemote(c, name)
	if err = saveConfig(*c); err != nil {
		return err
	}
	keyStatus := "kept"
	if removeLocalKey {
		if err = removeServiceKeyPair(serviceKey); err != nil {
			return fmt.Errorf("Jara was removed, but local key cleanup failed: %w", err)
		}
		keyStatus = "removed"
	} else if !keyPairExists && generatedServiceKey {
		keyStatus = "not found"
	} else if !generatedServiceKey {
		keyStatus = "user-provided key kept"
	}
	fmt.Printf("Jara was removed. Docker volumes were preserved unless --volumes was specified; local Jara access key pair: %s.\n", keyStatus)
	return nil
}

func serviceKeyPairExists(privatePath string) (bool, error) {
	for _, file := range []string{privatePath, privatePath + ".pub"} {
		if _, err := os.Lstat(file); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func removeServiceKeyPair(privatePath string) error {
	var failures []string
	for _, file := range []string{privatePath, privatePath + ".pub"} {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Sprintf("%s: %v", file, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func askYesFromStdin(prompt string, defaultYes bool) (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	return askYes(reader, prompt, defaultYes)
}

func forgetRemote(c *Config, name string) {
	delete(c.Remotes, name)
	delete(c.HostDeployments, name)
	for path, binding := range c.Bindings {
		if binding.Remote == name {
			delete(c.Bindings, path)
		}
	}
	for key, association := range c.Associations {
		if association.Remote == name {
			delete(c.Associations, key)
		}
	}
	if c.Default == name {
		c.Default = ""
		var names []string
		for remote := range c.Remotes {
			names = append(names, remote)
		}
		sort.Strings(names)
		if len(names) > 0 {
			c.Default = names[0]
		}
	}
}
