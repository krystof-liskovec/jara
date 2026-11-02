package jara

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func connectExistingJaraHost(reader *bufio.Reader) (Remote, error) {
	fmt.Println("Enter the SSH details for the existing Jara service. You can leave optional fields blank to use your SSH config or agent.")
	fmt.Println("Use the private key whose public key is authorized on the Jara host.")
	host, err := askSetup(reader, "Jara host address or SSH alias", "")
	if err != nil {
		return Remote{}, err
	}
	if host == "" {
		return Remote{}, fmt.Errorf("Jara host address or SSH alias is required")
	}

	portText, err := askSetup(reader, "Jara SSH port (blank uses SSH config; commonly 2222)", "")
	if err != nil {
		return Remote{}, err
	}
	port := 0
	if portText != "" {
		port, err = strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return Remote{}, fmt.Errorf("Jara SSH port must be between 1 and 65535")
		}
	}

	user, err := askSetup(reader, "Jara SSH username (commonly agent)", "")
	if err != nil {
		return Remote{}, err
	}
	identity, err := askSetupPath(reader, "Private SSH key path (blank uses SSH config or agent)", "")
	if err != nil {
		return Remote{}, err
	}
	if strings.HasPrefix(identity, "~/") {
		home, e := os.UserHomeDir()
		if e != nil {
			return Remote{}, e
		}
		identity = filepath.Join(home, identity[2:])
	}
	if identity != "" {
		identity, err = filepath.Abs(identity)
		if err != nil {
			return Remote{}, err
		}
	}

	remote := Remote{Host: host, Port: port, User: user, Identity: identity}
	if err = validateRemote(remote); err != nil {
		return Remote{}, err
	}
	return remote, nil
}
