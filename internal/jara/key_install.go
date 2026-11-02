package jara

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

func keyCommand(c Config, args []string) error {
	if len(args) == 0 || args[0] != "install" {
		return fmt.Errorf("use jara key install --public-key FILE [--remote NAME]")
	}
	f := flag.NewFlagSet("key install", flag.ContinueOnError)
	remoteName := f.String("remote", "", "guided deployment name")
	publicKeyPath := f.String("public-key", "", "public SSH key file")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 || *publicKeyPath == "" {
		return fmt.Errorf("use jara key install --public-key FILE [--remote NAME]")
	}

	name := *remoteName
	if name == "" {
		if _, ok := c.HostDeployments[c.Default]; ok {
			name = c.Default
		} else if len(c.HostDeployments) == 1 {
			for candidate := range c.HostDeployments {
				name = candidate
			}
		}
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("select a guided deployment with --remote NAME")
	}
	deployment, ok := c.HostDeployments[name]
	if !ok {
		if _, configured := c.Remotes[name]; configured {
			return fmt.Errorf("%q has no saved Docker-host admin connection; key install currently requires a guided deployment", name)
		}
		return fmt.Errorf("unknown remote %q", name)
	}

	key, fingerprint, err := readPublicKey(*publicKeyPath)
	if err != nil {
		return err
	}
	if err = installGuidedPublicKey(name, deployment, key); err != nil {
		return err
	}
	fmt.Printf("Installed %s on guided deployment %s. New SSH logins can use this key immediately.\n", fingerprint, name)
	return nil
}

func readPublicKey(filename string) (string, string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", "", fmt.Errorf("read public key: %w", err)
	}
	var key string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if key != "" {
			return "", "", fmt.Errorf("public key file must contain exactly one SSH key")
		}
		key = line
	}
	if key == "" || strings.ContainsAny(key, "\x00\r") {
		return "", "", fmt.Errorf("public key file is empty or invalid")
	}
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return "", "", fmt.Errorf("public key must contain an SSH key type and key data")
	}

	temp, err := os.CreateTemp("", "jara-public-key-*.pub")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.WriteString(key + "\n"); err != nil {
		_ = temp.Close()
		return "", "", err
	}
	if err = temp.Close(); err != nil {
		return "", "", err
	}
	output, err := exec.Command("ssh-keygen", "-lf", temp.Name()).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("invalid SSH public key: %s", strings.TrimSpace(string(output)))
	}
	fingerprintFields := strings.Fields(string(output))
	if len(fingerprintFields) < 2 {
		return "", "", fmt.Errorf("ssh-keygen returned no key fingerprint")
	}
	return key, fingerprintFields[1], nil
}

func installGuidedPublicKey(name string, deployment HostDeployment, key string) error {
	if !nameRE.MatchString(name) || !validGuidedDeploymentDirectory(name, deployment.Directory) {
		return fmt.Errorf("saved deployment directory is invalid; refusing to install a key")
	}
	if err := validateRemote(deployment.Admin); err != nil {
		return fmt.Errorf("invalid saved host connection: %w", err)
	}
	runner := hostSSHRunner{remote: deployment.Admin}
	homeData, err := runner.shell(20*time.Second, `printf '%s' "$HOME"`, nil, false)
	if err != nil {
		return fmt.Errorf("find Docker host home: %w", err)
	}
	home := strings.TrimSpace(string(homeData))
	if !path.IsAbs(home) || strings.ContainsAny(home, "\r\n\x00") {
		return fmt.Errorf("Docker host returned an invalid home directory")
	}
	root := path.Join(home, deployment.Directory)
	lock := path.Join(home, ".jara-deploy-"+name+".upgrade.lock")
	if _, err = runner.shell(20*time.Second, "mkdir -m 700 -- "+quote(lock), nil, false); err != nil {
		return fmt.Errorf("another deployment operation may be running; if not, inspect and remove %s before retrying: %w", lock, err)
	}
	defer func() {
		command := "rm -f -- " + quote(path.Join(lock, "owner")) + " && rmdir -- " + quote(lock)
		if _, releaseErr := runner.shell(10*time.Second, command, nil, false); releaseErr != nil {
			fmt.Fprintln(os.Stderr, "WARNING: could not release deployment lock:", releaseErr)
		}
	}()
	if _, err = runner.shell(10*time.Second, "printf '%s\\n' "+quote("install SSH key")+" > "+quote(path.Join(lock, "owner")), nil, false); err != nil {
		return fmt.Errorf("record deployment lock owner: %w", err)
	}

	manifestBytes, err := readHostFile(runner, path.Join(root, "deploy/.jara-native.json"))
	if err != nil {
		return fmt.Errorf("read guided deployment manifest: %w", err)
	}
	var manifest nativeDeployment
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode guided deployment manifest: %w", err)
	}
	if err = manifest.validate("jara-" + strings.ToLower(name)); err != nil {
		return err
	}

	keysPath := path.Join(root, "deploy/authorized_keys")
	keys, err := readHostFile(runner, keysPath)
	if err != nil {
		return fmt.Errorf("read deployment authorized keys: %w", err)
	}
	if !hasPublicKey(keys, key) {
		appendCommand := "printf '\\n%s\\n' " + quote(key) + " >> " + quote(keysPath)
		if _, err = runner.shell(20*time.Second, appendCommand, nil, false); err != nil {
			return fmt.Errorf("append public key to deployment: %w", err)
		}
	}
	keys, err = readHostFile(runner, keysPath)
	if err != nil {
		return fmt.Errorf("read updated deployment authorized keys: %w", err)
	}
	if !hasPublicKey(keys, key) {
		return fmt.Errorf("deployment authorized keys did not contain the requested key after update")
	}

	installScript := `set -eu
source=/run/jara/authorized_keys
directory=/etc/ssh/authorized_keys
target=$directory/agent
test -f "$source"
test ! -L "$source"
test -d "$directory"
test ! -L "$directory"
test ! -L "$target"
tmp=$(mktemp "$directory/.agent-keys.XXXXXXXX")
trap 'rm -f "$tmp"' EXIT
cp -- "$source" "$tmp"
chmod 0644 "$tmp"
chown root:root "$tmp"
mv -f -- "$tmp" "$target"
trap - EXIT
`
	if _, err = runner.run(30*time.Second, "", []string{"docker", "exec", "--user", "root", manifest.Container, "sh", "-c", installScript}, nil, false); err != nil {
		return fmt.Errorf("key is in the deployment source, but could not install it in the running agent; retry the command: %w", err)
	}
	installed, err := runner.run(20*time.Second, "", []string{"docker", "exec", "--user", "root", manifest.Container, "cat", "/etc/ssh/authorized_keys/agent"}, nil, false)
	if err != nil {
		return fmt.Errorf("key was copied to the running agent but could not be verified: %w", err)
	}
	if !bytes.Equal(installed, keys) {
		return fmt.Errorf("running agent authorized keys differ from the deployment source; retry the command")
	}
	return nil
}

func hasPublicKey(data []byte, key string) bool {
	wanted := strings.Fields(key)
	if len(wanted) < 2 {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == wanted[0] && fields[1] == wanted[1] {
			return true
		}
	}
	return false
}
