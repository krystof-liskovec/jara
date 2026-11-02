package jara

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func offerDeploymentConnection(c *Config, alias string, service Remote, reader *bufio.Reader) error {
	fmt.Println("Host management lets this computer upgrade Jara and manage SSH keys. It needs SSH access to the Linux machine running Docker, using an account allowed to run Docker.")
	enable, err := askYes(reader, "Enable host management from this computer?", true)
	if err != nil {
		return err
	}
	if !enable {
		fmt.Println("You can enable host management later by rerunning jara setup with this remote name.")
		return nil
	}
	admin, err := askDeploymentAdmin(reader, service)
	if err != nil {
		return err
	}
	name, err := askSetup(reader, "Original deployment name (chosen when Jara was installed)", alias)
	if err != nil {
		return err
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid deployment name")
	}
	directory, err := askSetup(reader, "Deployment directory relative to the Docker host login home", guidedDeploymentDirectory(name))
	if err != nil {
		return err
	}
	if !validGuidedDeploymentDirectory(name, directory) {
		return fmt.Errorf("expected %s or jara-deploy-%s", guidedDeploymentDirectory(name), name)
	}
	fmt.Println("Checking the existing deployment and its Jara SSH host key...")
	runner := hostSSHRunner{remote: admin}
	homeData, err := runner.shell(20*time.Second, `printf '%s' "$HOME"`, nil, false)
	if err != nil {
		return fmt.Errorf("connect to the Docker host: %w", err)
	}
	home := strings.TrimSpace(string(homeData))
	if !path.IsAbs(home) || strings.ContainsAny(home, "\r\n\x00") {
		return fmt.Errorf("Docker host returned an invalid home directory")
	}
	manifestData, err := readHostFile(runner, path.Join(home, directory, "deploy/.jara-native.json"))
	if err != nil {
		return fmt.Errorf("find the existing deployment manifest (manual Compose deployments require updates on the host): %w", err)
	}
	var manifest nativeDeployment
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("read deployment manifest: %w", err)
	}
	if err = validateDeploymentAttachment(name, directory, manifest); err != nil {
		return err
	}
	label, err := runner.run(20*time.Second, "", []string{"docker", "inspect", "--format", `{{index .Config.Labels "io.jara.project"}}`, manifest.Container}, nil, false)
	if err != nil {
		return fmt.Errorf("verify Docker access and the Jara container: %w", err)
	}
	if strings.TrimSpace(string(label)) != manifest.Project {
		return fmt.Errorf("container does not match the deployment manifest")
	}
	const hostKeyFile = "/var/lib/jara/ssh/ssh_host_ed25519_key.pub"
	containerKey, err := runner.run(20*time.Second, "", []string{"docker", "exec", manifest.Container, "cat", hostKeyFile}, nil, false)
	if err != nil {
		return err
	}
	serviceRunner := hostSSHRunner{remote: service}
	serviceKey, err := serviceRunner.run(20*time.Second, "", []string{"cat", hostKeyFile}, nil, false)
	if err != nil {
		return fmt.Errorf("read connected Jara server identity: %w", err)
	}
	if !sameDeploymentHostKey(containerKey, serviceKey) {
		return fmt.Errorf("this Docker deployment is a different Jara server; check the Docker host and original deployment name")
	}
	if c.HostDeployments == nil {
		c.HostDeployments = map[string]HostDeployment{}
	}
	c.HostDeployments[alias] = HostDeployment{Name: name, Admin: admin, Directory: directory, ServiceKeyMode: "existing"}
	if err = saveConfig(*c); err != nil {
		return err
	}
	fmt.Printf("Host management saved. Upgrade with `jara deployment upgrade %s`.\n", alias)
	return nil
}

func askDeploymentAdmin(reader *bufio.Reader, service Remote) (Remote, error) {
	fmt.Println("Enter the Docker host login details. Its SSH port is usually 22; the Jara service port is commonly 2222.")
	admin := Remote{}
	var err error
	admin.Host, err = askSetup(reader, "Docker host SSH address or alias", service.Host)
	if err != nil {
		return admin, err
	}
	admin.User, err = askSetup(reader, "Docker host login user (blank uses SSH config)", "")
	if err != nil {
		return admin, err
	}
	port, err := askSetup(reader, "Docker host SSH port (blank uses SSH config/default 22)", "")
	if err != nil {
		return admin, err
	}
	if port != "" {
		admin.Port, err = strconv.Atoi(port)
		if err != nil || admin.Port < 1 || admin.Port > 65535 {
			return admin, fmt.Errorf("host SSH port must be between 1 and 65535")
		}
	}
	admin.Identity = service.Identity
	if admin.Identity != "" {
		reuse, err := askYes(reader, "Use the same private SSH key as the Jara connection?", true)
		if err != nil {
			return admin, err
		}
		if !reuse {
			admin.Identity = ""
		}
	}
	if admin.Identity == "" {
		admin.Identity, err = askSetupPath(reader, "Docker host private key path (blank uses SSH config/agent)", "")
		if err != nil {
			return admin, err
		}
		if strings.HasPrefix(admin.Identity, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return admin, err
			}
			admin.Identity = filepath.Join(home, admin.Identity[2:])
		}
		if admin.Identity != "" {
			admin.Identity, err = filepath.Abs(admin.Identity)
			if err != nil {
				return admin, err
			}
		}
	}
	return admin, validateRemote(admin)
}

func validateDeploymentAttachment(name, directory string, manifest nativeDeployment) error {
	if !nameRE.MatchString(name) || !validGuidedDeploymentDirectory(name, directory) {
		return fmt.Errorf("invalid deployment name or directory")
	}
	if err := manifest.validate("jara-" + strings.ToLower(name)); err != nil {
		return err
	}
	if manifest.Name != name {
		return fmt.Errorf("manifest names deployment %q; enter that original deployment name in setup", manifest.Name)
	}
	return nil
}

func sameDeploymentHostKey(left, right []byte) bool {
	a, b := bytes.Fields(left), bytes.Fields(right)
	return len(a) >= 2 && len(b) >= 2 && bytes.Equal(a[0], []byte("ssh-ed25519")) &&
		bytes.Equal(a[0], b[0]) && bytes.Equal(a[1], b[1])
}
