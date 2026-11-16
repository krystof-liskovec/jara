package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/portable"
)

// Remote hosts that act as standbys receive the primary's checkpoints over
// SSH. Every host has a peer key; a standby authorizes the keys of the hosts
// ranked above it, and each of those may only run `jara _peer NAME`, which
// accepts a short list of sync and standby operations. Your client sets this
// up, since it already reaches every host.
//
// In the container deployment the agent user cannot change sshd's
// authorized_keys; sshd asks `jara _peer-keys` instead, which forces the peer
// command onto every key it lists, whatever the file says. Hosts without that
// hook get a managed block in ~/.ssh/authorized_keys with the same options.

// peerStandby is how a host reaches one standby ranked below it.
type peerStandby struct {
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port,omitempty"`
	User     string   `json:"user,omitempty"`
	HostKeys []string `json:"host_keys"`
}

var peerKeyRE = regexp.MustCompile(`^(ssh-ed25519|ecdsa-sha2-nistp256|ssh-rsa) [A-Za-z0-9+/]+={0,2}$`)

// peerRPCs are the operations a peer may run: transferring the workspace
// and storing standby checkpoints. Nothing that runs agents or shells.
var peerRPCs = map[string]bool{
	"git-state": true, "manifest": true, "exists": true, "apply": true, "guard": true,
	"standby-prepare": true, "standby-git": true, "standby-checkpoint": true,
}

func peerDir() string { return filepath.Join(fsutil.DataRoot(), "peers") }

func peerKeyPath() string { return filepath.Join(peerDir(), "id_ed25519") }

func peerSSHConfig() string { return filepath.Join(peerDir(), "ssh_config") }

// peerAlias is the SSH alias of a standby in peerSSHConfig.
func peerAlias(name string) string { return "jara-peer-" + name }

// peerRPC serves the peering setup your client runs on each host.
func peerRPC(q Request, body io.Reader) error {
	if err := os.MkdirAll(filepath.Join(peerDir(), "authorized"), 0700); err != nil {
		return err
	}
	out := json.NewEncoder(os.Stdout)
	switch q.Op {
	case "peer-key":
		if _, err := os.Stat(peerKeyPath()); os.IsNotExist(err) {
			cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "jara-peer", "-f", peerKeyPath())
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("create peer key: %w: %s", err, strings.TrimSpace(string(output)))
			}
		}
		key, err := os.ReadFile(peerKeyPath() + ".pub")
		if err != nil {
			return err
		}
		fields := strings.Fields(string(key))
		if len(fields) < 2 {
			return fmt.Errorf("unreadable peer key")
		}
		return out.Encode(map[string]string{"key": fields[0] + " " + fields[1]})
	case "peer-host-keys":
		return out.Encode(map[string][]string{"keys": hostPublicKeys()})
	case "peer-authorize":
		var request struct {
			Peers map[string]string `json:"peers"` // host name → public key
		}
		if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&request); err != nil {
			return err
		}
		return authorizePeers(request.Peers)
	case "peer-standbys":
		var standbys []peerStandby
		if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&standbys); err != nil {
			return err
		}
		return configureStandbys(standbys)
	}
	return fmt.Errorf("unknown RPC operation %s", q.Op)
}

// hostPublicKeys lists this host's sshd keys, for peers to pin.
func hostPublicKeys() []string {
	var keys []string
	for _, pattern := range []string{"/var/lib/jara/ssh/ssh_host_*_key.pub", "/etc/ssh/ssh_host_*_key.pub"} {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if fields := strings.Fields(string(data)); err == nil && len(fields) >= 2 && peerKeyRE.MatchString(fields[0]+" "+fields[1]) {
				keys = append(keys, fields[0]+" "+fields[1])
			}
		}
		if len(keys) > 0 {
			break // the deployment's own keys, when it has them
		}
	}
	return keys
}

// authorizePeers replaces the hosts allowed to forward to this one.
func authorizePeers(peers map[string]string) error {
	dir := filepath.Join(peerDir(), "authorized")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "*.pub"))
	for _, path := range old {
		if _, keep := peers[strings.TrimSuffix(filepath.Base(path), ".pub")]; !keep {
			_ = os.Remove(path)
		}
	}
	for name, key := range peers {
		if !nameRE.MatchString(name) || !peerKeyRE.MatchString(key) {
			return fmt.Errorf("invalid peer %q", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".pub"), []byte(key+"\n"), 0600); err != nil {
			return err
		}
	}
	if os.Getenv("JARA_PEER_KEYS_COMMAND") == "1" {
		return nil
	}
	return writeManagedAuthorizedKeys(peerAuthorizedLines(peerDir()))
}

// peerAuthorizedLines renders the authorized peers with their forced command.
func peerAuthorizedLines(dir string) []string {
	paths, _ := filepath.Glob(filepath.Join(dir, "authorized", "*.pub"))
	sort.Strings(paths)
	var lines []string
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".pub")
		data, err := os.ReadFile(path)
		key := strings.TrimSpace(string(data))
		if err != nil || !nameRE.MatchString(name) || !peerKeyRE.MatchString(key) {
			continue
		}
		lines = append(lines, fmt.Sprintf(`restrict,command="jara _peer %s" %s jara-peer:%s`, name, key, name))
	}
	return lines
}

// runPeerKeys is sshd's AuthorizedKeysCommand: the peers authorized in the
// data directory of the user whose home is home.
func runPeerKeys(home string) error {
	if !filepath.IsAbs(home) {
		return fmt.Errorf("invalid home directory")
	}
	dir := filepath.Join(home, ".local", "share", "jara", "peers")
	for _, line := range peerAuthorizedLines(dir) {
		fmt.Println(line)
	}
	return nil
}

const managedKeysBegin, managedKeysEnd = "# BEGIN jara peers (managed)", "# END jara peers"

// writeManagedAuthorizedKeys replaces Jara's block in ~/.ssh/authorized_keys.
func writeManagedAuthorizedKeys(lines []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".ssh", "authorized_keys")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var kept []string
	inBlock := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		switch {
		case line == managedKeysBegin:
			inBlock = true
		case line == managedKeysEnd:
			inBlock = false
		case !inBlock && line != "":
			kept = append(kept, line)
		}
	}
	if len(lines) > 0 {
		kept = append(kept, managedKeysBegin)
		kept = append(kept, lines...)
		kept = append(kept, managedKeysEnd)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return portable.WriteFileAtomic(path, []byte(strings.Join(kept, "\n")+"\n"))
}

// configureStandbys records the standbys ranked below this host and how to
// reach them: an SSH configuration with pinned host keys and the peer key.
func configureStandbys(standbys []peerStandby) error {
	var config, known strings.Builder
	for _, standby := range standbys {
		if !nameRE.MatchString(standby.Name) || validateRemote(Remote{Host: standby.Host, Port: standby.Port, User: standby.User}) != nil {
			return fmt.Errorf("invalid standby %q", standby.Name)
		}
		if strings.ContainsAny(standby.Host, " \t\"'#") || len(standby.HostKeys) == 0 {
			return fmt.Errorf("standby %s needs an address and host keys", standby.Name)
		}
		port := standby.Port
		if port == 0 {
			port = 22
		}
		fmt.Fprintf(&config, "Host %s\n  HostName %s\n  Port %d\n", peerAlias(standby.Name), standby.Host, port)
		if standby.User != "" {
			fmt.Fprintf(&config, "  User %s\n", standby.User)
		}
		fmt.Fprintf(&config, "  IdentityFile %s\n  IdentitiesOnly yes\n  UserKnownHostsFile %s\n  StrictHostKeyChecking yes\n  BatchMode yes\n",
			peerKeyPath(), filepath.Join(peerDir(), "known_hosts"))
		for _, key := range standby.HostKeys {
			if !peerKeyRE.MatchString(key) {
				return fmt.Errorf("invalid host key for %s", standby.Name)
			}
			fmt.Fprintf(&known, "[%s]:%s %s\n", standby.Host, strconv.Itoa(port), key)
		}
	}
	if err := portable.WriteFileAtomic(filepath.Join(peerDir(), "known_hosts"), []byte(known.String())); err != nil {
		return err
	}
	if err := portable.WriteFileAtomic(peerSSHConfig(), []byte(config.String())); err != nil {
		return err
	}
	return fsutil.WriteJSON(filepath.Join(peerDir(), "standbys.json"), standbys)
}

// loadStandbys lists the standbys this host forwards to, highest rank first.
func loadStandbys() []peerStandby {
	var standbys []peerStandby
	if data, err := os.ReadFile(filepath.Join(peerDir(), "standbys.json")); err == nil {
		_ = json.Unmarshal(data, &standbys)
	}
	return standbys
}

var peerCommandRE = regexp.MustCompile(`^'?jara'? '?_rpc'? '?([A-Za-z0-9_-]+)'?$`)

// runPeerCommand is the forced command of a peer's key: it runs the RPC the
// peer asked for when it is one of peerRPCs.
func runPeerCommand(peer string, stdin io.Reader) error {
	if !nameRE.MatchString(peer) {
		return fmt.Errorf("invalid peer")
	}
	match := peerCommandRE.FindStringSubmatch(strings.TrimSpace(os.Getenv("SSH_ORIGINAL_COMMAND")))
	if match == nil {
		return fmt.Errorf("peer %s may only run Jara standby operations", peer)
	}
	var q Request
	if err := decode(match[1], &q); err != nil {
		return err
	}
	if !peerRPCs[q.Op] {
		return fmt.Errorf("peer %s may not run %q", peer, q.Op)
	}
	q.Peer = peer
	if strings.HasPrefix(q.Op, "standby-") {
		return standbyRPC(q, bufio.NewReader(stdin))
	}
	return remoteRPC(q)
}
