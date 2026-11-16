package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// The standby chain is the top-ranked remote (the primary) followed by the
// remotes marked as standbys, in rank. Your client connects them: each host
// gets the peer keys of the hosts above it and the pinned addresses of the
// hosts below it. Remotes outside the chain are cleared.

func (c *Config) standbyChain() []string {
	ranked := c.rankedRemotes()
	if len(ranked) == 0 {
		return nil
	}
	chain := []string{ranked[0]}
	for _, name := range ranked[1:] {
		if c.Remotes[name].Standby {
			chain = append(chain, name)
		}
	}
	return chain
}

// remoteStandbyCommand is `jara remote standby NAME [--off] [--address HOST[:PORT]]`.
func remoteStandbyCommand(c *Config, args []string) error {
	usage := fmt.Errorf("use jara remote standby NAME [--off] [--address HOST[:PORT]]")
	if len(args) == 0 || !nameRE.MatchString(args[0]) {
		return usage
	}
	name := args[0]
	remote, ok := c.Remotes[name]
	if !ok {
		return fmt.Errorf("unknown remote %q", name)
	}
	remote.Standby = true
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--off":
			remote.Standby = false
		case args[i] == "--address" && i+1 < len(args):
			i++
			if _, _, err := splitPeerAddress(args[i]); err != nil {
				return err
			}
			remote.PeerHost = args[i]
		default:
			return usage
		}
	}
	c.Remotes[name] = remote
	if err := saveConfig(*c); err != nil {
		return err
	}
	if remote.Standby && c.rankedRemotes()[0] == name {
		fmt.Printf("%s is the primary (rank 1); it becomes a standby when another remote ranks above it.\n", name)
	}
	return setupPeers(*c)
}

// setupPeers connects the standby chain and clears hosts outside it.
func setupPeers(c Config) error {
	chain := c.standbyChain()
	inChain := map[string]bool{}
	for _, name := range chain {
		inChain[name] = true
	}
	failed := 0
	warn := func(name string, err error) {
		failed++
		if strings.Contains(err.Error(), "unknown RPC operation") {
			err = fmt.Errorf("its Jara is too old; upgrade it with jara deployment upgrade --remote %s", name)
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	}
	for _, name := range c.rankedRemotes() {
		if !inChain[name] || len(chain) == 1 {
			if err := configurePeer(c.Remotes[name], nil, nil); err != nil && len(chain) > 1 {
				warn(name, err)
			}
		}
	}
	if len(chain) < 2 {
		fmt.Println("No standby hosts; mark one with jara remote standby NAME.")
		return nil
	}
	keys := map[string]string{}
	for _, name := range chain[:len(chain)-1] {
		var answer struct {
			Key string `json:"key"`
		}
		if err := rpc(c.Remotes[name], Request{Op: "peer-key"}, nil, &answer); err != nil {
			warn(name, err)
			continue
		}
		keys[name] = answer.Key
	}
	standbys := map[string]peerStandby{}
	for _, name := range chain[1:] {
		standby, err := describeStandby(name, c.Remotes[name])
		if err != nil {
			warn(name, err)
			continue
		}
		standbys[name] = standby
	}
	for i, name := range chain {
		above := map[string]string{}
		for _, peer := range chain[:i] {
			if key, ok := keys[peer]; ok {
				above[peer] = key
			}
		}
		var below []peerStandby
		for _, peer := range chain[i+1:] {
			if standby, ok := standbys[peer]; ok {
				below = append(below, standby)
			}
		}
		if err := configurePeer(c.Remotes[name], above, below); err != nil {
			warn(name, err)
		}
	}
	fmt.Printf("Standby chain: %s.\n", strings.Join(chain, " → "))
	if failed > 0 {
		return fmt.Errorf("%d host(s) could not be set up; fix them and run jara remote order again", failed)
	}
	return nil
}

func configurePeer(remote Remote, above map[string]string, below []peerStandby) error {
	if above == nil {
		above = map[string]string{}
	}
	if below == nil {
		below = []peerStandby{}
	}
	body, _ := json.Marshal(map[string]any{"peers": above})
	if err := rpc(remote, Request{Op: "peer-authorize"}, bytes.NewReader(body), nil); err != nil {
		return err
	}
	body, _ = json.Marshal(below)
	return rpc(remote, Request{Op: "peer-standbys"}, bytes.NewReader(body), nil)
}

// describeStandby works out how hosts above reach a standby: its configured
// peer address, or what your SSH configuration resolves its name to, and the
// host keys it reports over your authenticated connection.
func describeStandby(name string, remote Remote) (peerStandby, error) {
	standby := peerStandby{Name: name, User: remote.User, Port: remote.Port}
	if remote.PeerHost != "" {
		host, port, err := splitPeerAddress(remote.PeerHost)
		if err != nil {
			return standby, err
		}
		standby.Host = host
		if port != 0 {
			standby.Port = port
		}
	} else {
		resolved, err := resolveSSHHost(remote)
		if err != nil {
			return standby, err
		}
		standby.Host = resolved["hostname"]
		if standby.Port == 0 {
			standby.Port, _ = strconv.Atoi(resolved["port"])
		}
		if standby.User == "" {
			standby.User = resolved["user"]
		}
		if jump := resolved["proxyjump"]; jump != "" && jump != "none" || isLoopback(standby.Host) {
			fmt.Fprintf(os.Stderr, "%s: your SSH configuration reaches it as %s%s; other hosts may not. Set the address they should use with jara remote standby %s --address HOST[:PORT].\n",
				name, standby.Host, map[bool]string{true: " through a jump host", false: ""}[jump != "" && jump != "none"], name)
		}
	}
	var answer struct {
		Keys []string `json:"keys"`
	}
	if err := rpc(remote, Request{Op: "peer-host-keys"}, nil, &answer); err != nil {
		return standby, err
	}
	if len(answer.Keys) == 0 {
		return standby, fmt.Errorf("it reported no SSH host keys")
	}
	standby.HostKeys = answer.Keys
	return standby, nil
}

// resolveSSHHost asks OpenSSH what your configuration makes of a remote.
func resolveSSHHost(remote Remote) (map[string]string, error) {
	args := []string{"-G"}
	if config := os.Getenv("JARA_SSH_CONFIG"); config != "" {
		args = append(args, "-F", config)
	}
	if remote.Port != 0 {
		args = append(args, "-p", strconv.Itoa(remote.Port))
	}
	if remote.User != "" {
		args = append(args, "-l", remote.User)
	}
	output, err := exec.Command("ssh", append(args, "--", remote.Host)...).Output()
	if err != nil {
		return nil, fmt.Errorf("resolve its SSH address: %w", err)
	}
	resolved := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			resolved[strings.ToLower(key)] = value
		}
	}
	if resolved["hostname"] == "" {
		return nil, fmt.Errorf("could not resolve its SSH address")
	}
	return resolved, nil
}

func splitPeerAddress(address string) (string, int, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		host, portText = address, ""
	}
	port := 0
	if portText != "" {
		if port, err = strconv.Atoi(portText); err != nil || port < 1 || port > 65535 {
			return "", 0, fmt.Errorf("invalid port in %q", address)
		}
	}
	if validateRemote(Remote{Host: host}) != nil || strings.ContainsAny(host, " \t\"'#") {
		return "", 0, fmt.Errorf("invalid address %q", address)
	}
	return host, port, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
