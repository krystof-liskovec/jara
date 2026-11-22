package jara

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/portable"
)

// Each host keeps the list of the network's hosts your client shares with it
// (jara network share): their addresses and pinned SSH host keys. With it,
// `jara connect` on one host can hop to another. The hop logs in with the
// SSH agent your phone forwards; no host gets a key for another.

func networkHostsPath() string { return filepath.Join(networkDir(), "hosts.json") }

func validNetworkHost(h networkHost) error {
	if !nameRE.MatchString(h.Name) || validateRemote(Remote{Host: h.Host, Port: h.Port, User: h.User}) != nil ||
		strings.HasPrefix(h.User, "-") || strings.ContainsAny(h.Host+h.User, "\"'#,[]@") {
		return fmt.Errorf("invalid host %q", h.Name)
	}
	if len(h.HostKeys) == 0 {
		return fmt.Errorf("host %s has no host keys", h.Name)
	}
	for _, key := range h.HostKeys {
		if !peerKeyRE.MatchString(key) {
			return fmt.Errorf("invalid host key for %s", h.Name)
		}
	}
	return nil
}

// saveNetworkHosts serves network-hosts: your client replacing the list.
func saveNetworkHosts(body io.Reader) error {
	var hosts []networkHost
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&hosts); err != nil {
		return err
	}
	for _, h := range hosts {
		if err := validNetworkHost(h); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(networkDir(), 0700); err != nil {
		return err
	}
	return fsutil.WriteJSON(networkHostsPath(), hosts)
}

// otherNetworkHosts lists the shared hosts except this one, which shares
// one of its SSH host keys.
func otherNetworkHosts() []networkHost {
	var hosts []networkHost
	data, err := os.ReadFile(networkHostsPath())
	if err != nil || json.Unmarshal(data, &hosts) != nil {
		return nil
	}
	return excludeHost(hosts, hostPublicKeys())
}

func excludeHost(hosts []networkHost, ownKeys []string) []networkHost {
	own := map[string]bool{}
	for _, key := range ownKeys {
		own[key] = true
	}
	var others []networkHost
	for _, h := range hosts {
		self := false
		for _, key := range h.HostKeys {
			self = self || own[key]
		}
		if !self && validNetworkHost(h) == nil {
			others = append(others, h)
		}
	}
	return others
}

func browseNetworkHosts() (menuItem, error) {
	hosts := otherNetworkHosts()
	rows := make([]menuItem, len(hosts))
	for i, h := range hosts {
		rows[i] = menuItem{Title: h.Name, Detail: networkHostAddress(h), Kind: "network-host", Group: h.Name}
	}
	for {
		n, err := selectMenu("Other hosts", rows)
		if err != nil {
			return menuItem{}, err
		}
		if problem := forwardedAgentProblem(); problem != "" {
			if err = showMenuNotice(rows[n].Title, problem,
				"Turn on agent forwarding for this host in your SSH client",
				"Termius: edit the host, enable Agent Forwarding, reconnect"); err != nil {
				return menuItem{}, err
			}
			continue
		}
		return rows[n], nil
	}
}

func networkHostAddress(h networkHost) string {
	address := h.Host
	if h.User != "" {
		address = h.User + "@" + address
	}
	if h.Port != 0 && h.Port != 22 {
		address += ":" + strconv.Itoa(h.Port)
	}
	return address
}

// forwardedAgentProblem says why the hop could not log in, or "".
func forwardedAgentProblem() string {
	if os.Getenv("SSH_AUTH_SOCK") == "" {
		return "Your SSH agent is not forwarded to this host"
	}
	err := exec.Command("ssh-add", "-l").Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "Your forwarded SSH agent holds no keys"
	}
	return ""
}

// hopToNetworkHost opens `jara connect` on another host of the network.
func hopToNetworkHost(name string) error {
	for _, h := range otherNetworkHosts() {
		if h.Name != name {
			continue
		}
		known := filepath.Join(networkDir(), "hop_known_hosts")
		if err := portable.WriteFileAtomic(known, []byte(hopKnownHosts(h))); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Connecting to %s…\n", name)
		return interactive(exec.Command("ssh", hopSSHArgs(h, known)...))
	}
	return fmt.Errorf("host %s is not in this host's network list; run jara network share on your computer", name)
}

func hopKnownHosts(h networkHost) string {
	pattern := h.Host
	if h.Port != 0 && h.Port != 22 {
		pattern = "[" + h.Host + "]:" + strconv.Itoa(h.Port)
	}
	var out strings.Builder
	for _, key := range h.HostKeys {
		fmt.Fprintf(&out, "%s %s\n", pattern, key)
	}
	return out.String()
}

// hopSSHArgs ignores this host's SSH configuration and known hosts: only the
// pinned keys are trusted, and only the forwarded agent authenticates.
func hopSSHArgs(h networkHost, known string) []string {
	args := []string{"-t", "-F", "/dev/null",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "ForwardAgent=no"}
	if h.Port != 0 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	if h.User != "" {
		args = append(args, "-l", h.User)
	}
	return append(args, "--", h.Host, "jara connect")
}

// shareNetworkHosts gives every host the list of your hosts, so its
// `jara connect` menu can hop to the others.
func shareNetworkHosts(c Config) error {
	var hosts []networkHost
	for _, name := range c.rankedRemotes() {
		host, err := networkHostFor(name, c.Remotes[name], "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Leaving out %s: %v\n", name, err)
			continue
		}
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		return errors.New("no remote host could be described")
	}
	body, _ := json.Marshal(hosts)
	failed := 0
	for _, name := range c.rankedRemotes() {
		if err := rpc(c.Remotes[name], Request{Op: "network-hosts"}, bytes.NewReader(body), nil); err != nil {
			if strings.Contains(err.Error(), "unknown RPC operation") {
				err = fmt.Errorf("its Jara is too old; upgrade it with jara deployment upgrade --remote %s", name)
			}
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			failed++
		}
	}
	fmt.Printf("Shared %d host(s) with your hosts; jara connect on one now offers the others.\n", len(hosts))
	if failed > 0 {
		return fmt.Errorf("%d host(s) did not take the list", failed)
	}
	return nil
}
