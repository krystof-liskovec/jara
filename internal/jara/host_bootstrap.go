package jara

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func provisionDockerHost(c *Config, name string, reader *bufio.Reader) (Remote, error) {
	if len(deploymentBundle()) == 0 {
		return Remote{}, fmt.Errorf("this Jara build does not include host deployment assets; run setup from your local Jara CLI")
	}
	fmt.Println("This builds and runs Jara as a Docker container on the host over SSH. The host needs Docker access, but no Jara CLI installation.")
	host, err := askSetup(reader, "Docker host SSH alias/address", "")
	if err != nil || host == "" {
		return Remote{}, fmt.Errorf("Docker host SSH address is required")
	}
	adminUser, err := askSetup(reader, "Host login user (blank uses SSH config)", "")
	if err != nil {
		return Remote{}, err
	}
	adminPortText, err := askSetup(reader, "Host SSH port (blank uses SSH config/default)", "")
	if err != nil {
		return Remote{}, err
	}
	adminPort := 0
	if adminPortText != "" {
		adminPort, err = strconv.Atoi(adminPortText)
		if err != nil || adminPort < 1 || adminPort > 65535 {
			return Remote{}, fmt.Errorf("host SSH port must be between 1 and 65535")
		}
	}
	adminIdentity, err := askSetup(reader, "Host login private key path (blank uses SSH config/agent)", "")
	if err != nil {
		return Remote{}, err
	}
	if strings.HasPrefix(adminIdentity, "~/") {
		home, e := os.UserHomeDir()
		if e != nil {
			return Remote{}, e
		}
		adminIdentity = filepath.Join(home, adminIdentity[2:])
	}
	if adminIdentity != "" {
		adminIdentity, err = filepath.Abs(adminIdentity)
		if err != nil {
			return Remote{}, err
		}
	}
	admin := Remote{Host: host, User: adminUser, Port: adminPort, Identity: adminIdentity}
	if err = validateRemote(admin); err != nil {
		return Remote{}, err
	}
	servicePortText, err := askSetup(reader, "Jara SSH port on the host", "2222")
	if err != nil {
		return Remote{}, err
	}
	servicePort, err := strconv.Atoi(servicePortText)
	if err != nil || servicePort < 1 || servicePort > 65535 {
		return Remote{}, fmt.Errorf("Jara SSH port must be between 1 and 65535")
	}
	bind, err := askSetup(reader, "IPv4 bind address for Jara (0.0.0.0 exposes it; restrict with firewall)", "0.0.0.0")
	if err != nil {
		return Remote{}, err
	}
	parsedBind := net.ParseIP(bind)
	if parsedBind == nil || parsedBind.To4() == nil {
		return Remote{}, fmt.Errorf("host bind address must be an IPv4 address")
	}
	fmt.Println("Checking Docker and archive extraction on the host...")
	check := sshCommand(admin, false, shellArgs("sh", "-c", "command -v tar >/dev/null && docker info >/dev/null"))
	check.Stdin, check.Stdout, check.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err = check.Run(); err != nil {
		return Remote{}, fmt.Errorf("host needs tar and permission to run Docker; no deployment was made: %w", err)
	}
	fmt.Printf("Ready to transfer the Jara source bundled in this CLI to ~/%s, expose port %d on %s, and build/start the container.\n", guidedDeploymentDirectory(name), servicePort, bind)
	fmt.Println("A public SSH key will be authorized for the Jara container. The Docker host's SSH setup is unchanged.")
	confirmed, err := askYes(reader, "Deploy Jara to this host now?", false)
	if err != nil {
		return Remote{}, err
	}
	if !confirmed {
		return Remote{}, fmt.Errorf("deployment cancelled; host was not changed")
	}
	serviceKey, publicKey, serviceKeyMode, err := chooseJaraServiceKey(name, admin.Identity, reader)
	if err != nil {
		return Remote{}, err
	}
	service := Remote{Host: host, User: "agent", Port: servicePort, Identity: serviceKey}
	if err = validateRemote(service); err != nil {
		return Remote{}, err
	}
	if err = deployJara(admin, name, servicePort, bind, publicKey); err != nil {
		return Remote{}, err
	}
	if c.HostDeployments == nil {
		c.HostDeployments = map[string]HostDeployment{}
	}
	c.HostDeployments[name] = HostDeployment{Admin: admin, Directory: guidedDeploymentDirectory(name), ServiceKeyMode: serviceKeyMode}
	fmt.Printf("Jara is starting on %s:%d. Allow that port through the host firewall if needed.\n", host, servicePort)
	return service, nil
}

func jaraServiceKey(name string) (string, error) {
	path, err := jaraServiceKeyPath(name)
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(path); err == nil {
		if _, e := os.Stat(path + ".pub"); e == nil {
			return path, nil
		}
		return "", fmt.Errorf("%s exists without a public key; choose another key path or repair it", path)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", path, "-C", "jara-"+name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		return "", err
	}
	return path, nil
}

func chooseJaraServiceKey(name, defaultPath string, reader *bufio.Reader) (string, string, string, error) {
	useExisting, err := askYes(reader, "Use an existing SSH key pair for Jara?", false)
	if err != nil {
		return "", "", "", err
	}
	if !useExisting {
		privatePath, e := jaraServiceKey(name)
		if e != nil {
			return "", "", "", e
		}
		publicKey, e := os.ReadFile(privatePath + ".pub")
		if e != nil {
			return "", "", "", fmt.Errorf("read generated Jara public key: %w", e)
		}
		if _, _, e = keyMaterial(publicKey); e != nil {
			return "", "", "", fmt.Errorf("invalid public key in %s.pub: %w", privatePath, e)
		}
		return privatePath, strings.TrimSpace(string(publicKey)), "generated", nil
	}
	privatePath, err := askSetup(reader, "Existing private SSH key path (public key will be derived)", defaultPath)
	if err != nil {
		return "", "", "", err
	}
	if privatePath == "" {
		return "", "", "", fmt.Errorf("private SSH key path is required")
	}
	if strings.HasPrefix(privatePath, "~/") {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", "", "", e
		}
		privatePath = filepath.Join(home, privatePath[2:])
	}
	privatePath, err = filepath.Abs(privatePath)
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Stat(privatePath)
	if err != nil {
		return "", "", "", fmt.Errorf("open existing private key: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", "", fmt.Errorf("existing private key must be a regular file")
	}
	var derived bytes.Buffer
	cmd := exec.Command("ssh-keygen", "-y", "-f", privatePath)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, &derived, os.Stderr
	if err = cmd.Run(); err != nil {
		return "", "", "", fmt.Errorf("derive public key from %s: %w", privatePath, err)
	}
	derivedKey := strings.TrimSpace(derived.String())
	derivedType, derivedBlob, err := keyMaterial([]byte(derivedKey))
	if err != nil {
		return "", "", "", fmt.Errorf("ssh-keygen returned an invalid public key: %w", err)
	}
	if public, e := os.ReadFile(privatePath + ".pub"); e == nil {
		publicType, publicBlob, e := keyMaterial(public)
		if e != nil || publicType != derivedType || publicBlob != derivedBlob {
			return "", "", "", fmt.Errorf("%s does not match its .pub file", privatePath)
		}
	} else if !os.IsNotExist(e) {
		return "", "", "", e
	}
	return privatePath, derivedKey, "existing", nil
}

func keyMaterial(publicKey []byte) (string, string, error) {
	fields := strings.Fields(string(publicKey))
	if len(fields) < 2 {
		return "", "", fmt.Errorf("expected an SSH key type and key data")
	}
	return fields[0], fields[1], nil
}

func jaraServiceKeyPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "id_ed25519_jara_"+name), nil
}

func deployJara(admin Remote, name string, port int, bind, publicKey string) error {
	dir := "$HOME/" + guidedDeploymentDirectory(name)
	quotedDir := `"` + dir + `"`
	stage, err := os.MkdirTemp("", "jara-host-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = os.MkdirAll(filepath.Join(stage, "deploy"), 0700); err != nil {
		return err
	}
	envPath := filepath.Join(stage, "deploy", ".env")
	if err = environment([]string{"--out", envPath}); err != nil {
		return fmt.Errorf("generate deployment settings: %w", err)
	}
	env, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	text := strings.Replace(string(env), "JARA_SSH_BIND=127.0.0.1", "JARA_SSH_BIND="+bind, 1)
	text = strings.Replace(text, "JARA_SSH_PORT=2222", "JARA_SSH_PORT="+strconv.Itoa(port), 1)
	if net.ParseIP(bind) == nil || strings.ContainsAny(bind, "\r\n\x00") || !strings.Contains(text, "JARA_SSH_BIND="+bind) {
		return fmt.Errorf("invalid Jara bind address")
	}
	config, err := parseGeneratedEnvironment([]byte(text))
	if err != nil {
		return err
	}
	deployment := newNativeDeployment(name, bind, port)
	manifest, err := nativeManifestBytes(deployment)
	if err != nil {
		return err
	}
	settings, err := packSettings(text, publicKey, manifest)
	if err != nil {
		return err
	}
	if err = transferArchive(admin, `test ! -e `+quotedDir+` && mkdir -p `+quotedDir+` && tar -xzf - -C `+quotedDir, deploymentBundle()); err != nil {
		return fmt.Errorf("transfer bundled Jara source to host: %w", err)
	}
	if err = transferArchive(admin, `tar -xzf - -C `+quotedDir, settings); err != nil {
		return fmt.Errorf("copy deployment settings to host: %w", err)
	}
	build := "cd " + quotedDir + " && " + renderNativeShellArgs(nativeBuildArgs(config, deployment.Image, ""))
	if err = runBootstrapSSH(admin, build, nil); err != nil {
		return fmt.Errorf("build Jara image: %w", err)
	}
	runWithAppArmor := renderNativeShellArgs(nativeRunArgs(deployment, config, deployment.Image, nativeDeployDirToken, "unconfined"))
	runWithoutAppArmor := renderNativeShellArgs(nativeRunArgs(deployment, config, deployment.Image, nativeDeployDirToken, ""))
	run := "cd " + quotedDir + " && security_options=$(docker info --format '{{json .SecurityOptions}}') && case \"$security_options\" in *name=apparmor*) " + runWithAppArmor + " ;; *) " + runWithoutAppArmor + " ;; esac"
	if err = runBootstrapSSH(admin, run, nil); err != nil {
		return fmt.Errorf("start Jara container: %w", err)
	}
	return nil
}

func parseGeneratedEnvironment(data []byte) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid generated setting %q", line)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func packSettings(env, publicKey string, manifest []byte) ([]byte, error) {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	files := []struct {
		name string
		data []byte
	}{
		{"deploy/.env", []byte(env)},
		{"deploy/authorized_keys", []byte(publicKey + "\n")},
		{"deploy/.jara-native.json", manifest},
	}
	for _, file := range files {
		header := &tar.Header{Name: file.name, Mode: 0600, Size: int64(len(file.data)), Typeflag: tar.TypeReg}
		if err := archive.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := archive.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := compressed.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func transferArchive(host Remote, command string, archive []byte) error {
	if len(archive) == 0 {
		return fmt.Errorf("empty deployment archive")
	}
	input := bytes.NewReader(archive)
	return runBootstrapSSH(host, command, input)
}

func runBootstrapSSH(host Remote, command string, input io.Reader) error {
	cmd := sshCommand(host, false, shellArgs("sh", "-c", command))
	cmd.Stdin = input
	if input == nil {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
