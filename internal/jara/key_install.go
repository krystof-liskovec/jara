package jara

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
	"unicode"
)

func keyCommand(c Config, args []string) error {
	if len(args) == 0 || args[0] != "install" && args[0] != "list" && args[0] != "remove" {
		return fmt.Errorf("use jara key install|list|remove [OPTIONS]")
	}
	action := args[0]
	f := flag.NewFlagSet("key "+action, flag.ContinueOnError)
	remoteName := f.String("remote", "", "guided deployment name")
	publicKeyPath := f.String("public-key", "", "public SSH key file")
	fingerprint := f.String("fingerprint", "", "SHA256 fingerprint from jara key list")
	connectOnly := f.Bool("connect-only", false, "force this key to open Jara's workspace menu")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected argument")
	}
	switch action {
	case "install":
		if *publicKeyPath == "" || *fingerprint != "" {
			return fmt.Errorf("use jara key install --public-key FILE [--connect-only] [--remote NAME]")
		}
	case "list":
		if *publicKeyPath != "" || *fingerprint != "" || *connectOnly {
			return fmt.Errorf("use jara key list [--remote NAME]")
		}
	case "remove":
		if *fingerprint == "" || *publicKeyPath != "" || *connectOnly {
			return fmt.Errorf("use jara key remove --fingerprint SHA256:... [--remote NAME]")
		}
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
			return fmt.Errorf("%q has no saved Docker-host admin connection; key management currently requires a guided deployment", name)
		}
		return fmt.Errorf("unknown remote %q", name)
	}

	var key, keyFingerprint string
	var err error
	if action == "install" {
		key, keyFingerprint, err = readPublicKey(*publicKeyPath)
		if err != nil {
			return err
		}
	}
	if action == "remove" && !validKeyFingerprint(*fingerprint) {
		return fmt.Errorf("fingerprint must use the SHA256:... format shown by jara key list")
	}

	return withGuidedKeyStore(name, deployment, func(runner hostSSHRunner, manifest nativeDeployment, keysPath string, keys []byte) error {
		switch action {
		case "install":
			connectEntry := hasConnectOnlyAuthorizedKey(keys, key)
			if hasPublicKey(keys, key) && connectEntry != *connectOnly {
				return fmt.Errorf("this key already has different SSH access; use a separate phone key to keep VS Code unrestricted")
			}
			entry := key
			if *connectOnly {
				entry = connectOnlyKeyPrefix + key
			}
			updated := appendAuthorizedKey(keys, entry)
			if err := updateGuidedKeys(runner, manifest, keysPath, keys, updated); err != nil {
				return err
			}
			access := ""
			if *connectOnly {
				access = " with Jara connect-only access"
			}
			fmt.Printf("Installed %s%s on guided deployment %s.\n", keyFingerprint, access, name)
			return nil
		case "list":
			if err := verifyAgentKeys(runner, manifest, keys); err != nil {
				return err
			}
			return printAuthorizedKeys(name, keys)
		case "remove":
			updated, removed, err := removeAuthorizedKey(keys, *fingerprint)
			if err != nil {
				return err
			}
			if removed == 0 {
				if err = verifyAgentKeys(runner, manifest, keys); err != nil {
					if err = synchronizeAgentKeys(runner, manifest, keys); err != nil {
						return err
					}
					fmt.Printf("Key %s was already absent from the source; synchronized the live agent.\n", *fingerprint)
					return nil
				}
				return fmt.Errorf("no key with fingerprint %s is installed on %s", *fingerprint, name)
			}
			if err = updateGuidedKeys(runner, manifest, keysPath, keys, updated); err != nil {
				return err
			}
			fmt.Printf("Removed %d key entr%s (%s) from guided deployment %s.\n", removed, pluralSuffix(removed), *fingerprint, name)
			return nil
		default:
			return fmt.Errorf("unknown key operation")
		}
	})
}

func withGuidedKeyStore(name string, deployment HostDeployment, action func(hostSSHRunner, nativeDeployment, string, []byte) error) error {
	name = deploymentName(name, deployment)
	if !nameRE.MatchString(name) || !validGuidedDeploymentDirectory(name, deployment.Directory) {
		return fmt.Errorf("saved deployment directory is invalid; refusing key management")
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
	if _, err = runner.shell(10*time.Second, "printf '%s\\n' "+quote("manage SSH keys")+" > "+quote(path.Join(lock, "owner")), nil, false); err != nil {
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
	if manifest.Name != name {
		return fmt.Errorf("guided deployment manifest name does not match %q", name)
	}
	if err = manifest.validate("jara-" + strings.ToLower(name)); err != nil {
		return err
	}
	keysPath := path.Join(root, "deploy/authorized_keys")
	keys, err := readHostFile(runner, keysPath)
	if err != nil {
		return fmt.Errorf("read deployment authorized keys: %w", err)
	}
	return action(runner, manifest, keysPath, keys)
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
	fingerprint, err := publicKeyFingerprint(key)
	if err != nil {
		return "", "", err
	}
	return key, fingerprint, nil
}

func publicKeyFingerprint(key string) (string, error) {
	fields, keyIndex, err := authorizedKeyFields(key)
	if err != nil {
		return "", err
	}
	temp, err := os.CreateTemp("", "jara-public-key-*.pub")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.WriteString(fields[keyIndex] + " " + fields[keyIndex+1] + "\n"); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err = temp.Close(); err != nil {
		return "", err
	}
	output, err := exec.Command("ssh-keygen", "-lf", temp.Name()).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("invalid SSH public key: %s", strings.TrimSpace(string(output)))
	}
	fingerprintFields := strings.Fields(string(output))
	if len(fingerprintFields) < 2 {
		return "", fmt.Errorf("ssh-keygen returned no key fingerprint")
	}
	return fingerprintFields[1], nil
}

func appendAuthorizedKey(keys []byte, key string) []byte {
	if hasPublicKey(keys, key) {
		return keys
	}
	updated := append([]byte(nil), keys...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, key...)
	return append(updated, '\n')
}

func hasPublicKey(data []byte, key string) bool {
	wanted, wantedIndex, err := authorizedKeyFields(key)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields, keyIndex, err := authorizedKeyFields(line)
		if err == nil && fields[keyIndex] == wanted[wantedIndex] && fields[keyIndex+1] == wanted[wantedIndex+1] {
			return true
		}
	}
	return false
}

func hasConnectOnlyAuthorizedKey(data []byte, key string) bool {
	wanted, wantedIndex, err := authorizedKeyFields(key)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, connectOnlyKeyPrefix) {
			continue
		}
		fields, keyIndex, err := authorizedKeyFields(strings.TrimPrefix(line, connectOnlyKeyPrefix))
		if err == nil && fields[keyIndex] == wanted[wantedIndex] && fields[keyIndex+1] == wanted[wantedIndex+1] {
			return true
		}
	}
	return false
}

func authorizedKeyFields(line string) ([]string, int, error) {
	fields := strings.Fields(line)
	for i := 0; i+1 < len(fields); i++ {
		if !isAuthorizedKeyType(fields[i]) {
			continue
		}
		if validAuthorizedKeyData(fields[i], fields[i+1]) {
			return fields, i, nil
		}
	}
	return nil, 0, fmt.Errorf("public key must contain an SSH key type and key data")
}

func validAuthorizedKeyData(keyType, encoded string) bool {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(data) < 4 {
		return false
	}
	typeLength := binary.BigEndian.Uint32(data[:4])
	if uint64(typeLength) > uint64(len(data)-4) {
		return false
	}
	return string(data[4:4+int(typeLength)]) == keyType
}

func isAuthorizedKeyType(value string) bool {
	return strings.HasPrefix(value, "ssh-") || strings.HasPrefix(value, "ecdsa-sha2-") || strings.HasPrefix(value, "sk-")
}

const connectOnlyKeyPrefix = `command="/usr/local/bin/jara connect",no-port-forwarding `

func updateGuidedKeys(runner hostSSHRunner, manifest nativeDeployment, keysPath string, previous, updated []byte) error {
	if !bytes.Equal(previous, updated) {
		command := "test -f " + quote(keysPath) + " && test ! -L " + quote(keysPath) + " && cat > " + quote(keysPath)
		_, writeErr := runner.shell(30*time.Second, command, bytes.NewReader(updated), false)
		actual, readErr := readHostFile(runner, keysPath)
		if readErr != nil || !bytes.Equal(actual, updated) {
			restoreErr := restoreHostKeys(runner, keysPath, previous)
			if restoreErr != nil {
				return fmt.Errorf("could not verify updated keys or restore the previous file: write=%v read=%v restore=%w", writeErr, readErr, restoreErr)
			}
			if writeErr != nil {
				return fmt.Errorf("update authorized keys failed; previous file restored: %w", writeErr)
			}
			return fmt.Errorf("updated authorized keys did not match the requested content; previous file restored")
		}
	}
	return synchronizeAgentKeys(runner, manifest, updated)
}

func restoreHostKeys(runner hostSSHRunner, keysPath string, previous []byte) error {
	command := "test -f " + quote(keysPath) + " && test ! -L " + quote(keysPath) + " && cat > " + quote(keysPath)
	if _, err := runner.shell(30*time.Second, command, bytes.NewReader(previous), false); err != nil {
		return err
	}
	actual, err := readHostFile(runner, keysPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, previous) {
		return fmt.Errorf("restored authorized keys do not match the original")
	}
	return nil
}

func synchronizeAgentKeys(runner hostSSHRunner, manifest nativeDeployment, keys []byte) error {
	installScript := `set -eu
source=/run/jara/authorized_keys
directory=/etc/ssh/authorized_keys
target=$directory/` + nativeSSHUser(manifest) + `
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
	if _, err := runner.run(30*time.Second, "", []string{"docker", "exec", "--user", "root", manifest.Container, "sh", "-c", installScript}, nil, false); err != nil {
		return fmt.Errorf("deployment key file changed but could not synchronize the running agent; retry the command: %w", err)
	}
	return verifyAgentKeys(runner, manifest, keys)
}

func verifyAgentKeys(runner hostSSHRunner, manifest nativeDeployment, keys []byte) error {
	installed, err := runner.run(20*time.Second, "", []string{"docker", "exec", "--user", "root", manifest.Container, "cat", "/etc/ssh/authorized_keys/" + nativeSSHUser(manifest)}, nil, false)
	if err != nil {
		return fmt.Errorf("could not read authorized keys from the running agent: %w", err)
	}
	if !bytes.Equal(installed, keys) {
		return fmt.Errorf("running agent authorized keys differ from the deployment source; run a key install or remove command to synchronize them")
	}
	return nil
}

type listedKey struct {
	fingerprint string
	comment     string
}

func parseAuthorizedKeys(data []byte) ([]listedKey, error) {
	var keys []listedKey
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fingerprint, err := publicKeyFingerprint(line)
		if err != nil {
			return nil, fmt.Errorf("cannot read an authorized key entry: %w", err)
		}
		fields, keyIndex, _ := authorizedKeyFields(line)
		comment := ""
		if len(fields) > keyIndex+2 {
			comment = safeKeyComment(strings.Join(fields[keyIndex+2:], " "))
		}
		keys = append(keys, listedKey{fingerprint: fingerprint, comment: comment})
	}
	return keys, nil
}

func printAuthorizedKeys(name string, data []byte) error {
	keys, err := parseAuthorizedKeys(data)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		fmt.Printf("No authorized SSH keys on %s.\n", name)
		return nil
	}
	fmt.Printf("Authorized SSH keys on %s:\n", name)
	for _, key := range keys {
		comment := key.comment
		if comment == "" {
			comment = "(no comment)"
		}
		fmt.Printf("%s\t%s\n", key.fingerprint, comment)
	}
	return nil
}

func removeAuthorizedKey(data []byte, fingerprint string) ([]byte, int, error) {
	var updated strings.Builder
	removed, remaining := 0, 0
	for _, chunk := range strings.SplitAfter(string(data), "\n") {
		if chunk == "" {
			continue
		}
		line := strings.TrimSpace(strings.TrimSuffix(chunk, "\n"))
		if line == "" || strings.HasPrefix(line, "#") {
			updated.WriteString(chunk)
			continue
		}
		lineFingerprint, err := publicKeyFingerprint(line)
		if err != nil {
			return nil, 0, fmt.Errorf("cannot remove keys from an unrecognized authorized-key entry: %w", err)
		}
		if lineFingerprint == fingerprint {
			removed++
			continue
		}
		remaining++
		updated.WriteString(chunk)
	}
	if removed == 0 {
		return data, 0, nil
	}
	if remaining == 0 {
		return nil, 0, fmt.Errorf("cannot remove the last authorized SSH key; install another key first")
	}
	return []byte(updated.String()), removed, nil
}

func validKeyFingerprint(fingerprint string) bool {
	if !strings.HasPrefix(fingerprint, "SHA256:") || strings.ContainsAny(fingerprint, " \t\r\n") {
		return false
	}
	return len(fingerprint) > len("SHA256:")
}

func safeKeyComment(comment string) string {
	comment = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, comment)
	comment = strings.Join(strings.Fields(comment), " ")
	runes := []rune(comment)
	if len(runes) > 120 {
		comment = string(runes[:117]) + "..."
	}
	return comment
}

func pluralSuffix(count int) string {
	if count == 1 {
		return "y"
	}
	return "ies"
}
