package jara

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/portable"
)

const networkUsage = "use jara network [status] | invite [--remote NAME] [--address HOST[:PORT]] [--minutes N] | devices [--remote NAME] | revoke DEVICE | share"

// networkCommand is `jara network`: inviting devices into your network of
// remote hosts, and listing or removing the devices that joined.
func networkCommand(c Config, args []string) error {
	if len(args) == 0 {
		return networkOverview(c)
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New(networkUsage)
		}
		return networkOverview(c)
	case "invite":
		f := flag.NewFlagSet("network invite", flag.ContinueOnError)
		remote := f.String("remote", "", "the host the new device connects to first (default: the primary)")
		address := f.String("address", "", "how the new device reaches that host, when your SSH configuration differs")
		minutes := f.Int("minutes", 15, "how long the code works")
		if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 || *minutes < 1 || *minutes > 60 {
			return errors.New(networkUsage)
		}
		return inviteDevice(c, *remote, *address, time.Duration(*minutes)*time.Minute)
	case "devices":
		f := flag.NewFlagSet("network devices", flag.ContinueOnError)
		remote := f.String("remote", "", "the host to ask (default: the primary)")
		if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
			return errors.New(networkUsage)
		}
		name, r, err := resolveConfigRemote(c, firstNonEmpty(*remote, c.Default))
		if err != nil {
			return err
		}
		var devices []string
		if err = rpc(r, Request{Op: "network-devices"}, nil, &devices); err != nil {
			return err
		}
		fmt.Printf("Devices that joined through a code, as %s knows them: %s\n", name, strings.Join(devices, ", "))
		return nil
	case "share":
		if len(args) != 1 {
			return errors.New(networkUsage)
		}
		return shareNetworkHosts(c)
	case "revoke":
		if len(args) != 2 || !deviceNameOK.MatchString(args[1]) {
			return errors.New(networkUsage)
		}
		for _, name := range c.rankedRemotes() {
			if err := rpc(c.Remotes[name], Request{Op: "network-revoke", Runner: args[1]}, nil, nil); err != nil {
				fmt.Printf("%s: %v\n", name, err)
			} else {
				fmt.Printf("%s: %s can no longer log in.\n", name, args[1])
			}
		}
		return nil
	}
	return errors.New(networkUsage)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// networkHostFor describes how another device reaches one of your remotes:
// its real address (not your SSH alias) and its host keys.
func networkHostFor(name string, remote Remote, address string) (networkHost, error) {
	host := networkHost{Name: name, Standby: remote.Standby}
	if address == "" {
		address = remote.PeerHost
	}
	if address != "" {
		var err error
		if host.Host, host.Port, err = splitPeerAddress(address); err != nil {
			return host, err
		}
		host.User = remote.User
	} else {
		resolved, err := resolveSSHHost(remote)
		if err != nil {
			return host, err
		}
		host.Host, host.User = resolved["hostname"], resolved["user"]
		host.Port, _ = strconv.Atoi(resolved["port"])
	}
	if isLoopback(host.Host) {
		return host, fmt.Errorf("you reach it at %s, which other devices cannot; pass --address (for example its Tailscale name)", host.Host)
	}
	var answer struct {
		Keys []string `json:"keys"`
	}
	if err := rpc(remote, Request{Op: "peer-host-keys"}, nil, &answer); err != nil {
		return host, fmt.Errorf("read its host keys: %w", err)
	}
	if len(answer.Keys) == 0 {
		return host, fmt.Errorf("it reported no host keys")
	}
	host.HostKeys = answer.Keys
	return host, nil
}

func inviteDevice(c Config, remoteName, address string, lifetime time.Duration) error {
	ranked := c.rankedRemotes()
	if len(ranked) == 0 {
		return errors.New("add a remote host first; devices join through one")
	}
	remoteName = firstNonEmpty(remoteName, ranked[0])
	entry, ok := c.Remotes[remoteName]
	if !ok {
		return fmt.Errorf("remote %s is not configured", remoteName)
	}
	first, err := networkHostFor(remoteName, entry, address)
	if err != nil {
		return fmt.Errorf("%s: %w", remoteName, err)
	}
	registry := []networkHost{first}
	for _, name := range ranked {
		if name == remoteName {
			continue
		}
		host, hostErr := networkHostFor(name, c.Remotes[name], "")
		if hostErr != nil {
			fmt.Printf("Leaving out %s: %v\n", name, hostErr)
			continue
		}
		registry = append(registry, host)
	}
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	key := authorizedKey(joinKey(secret).Public().(ed25519.PublicKey))
	expires := time.Now().Add(lifetime)
	body, _ := json.Marshal(networkInvite{Key: key, Expires: expires.Unix(), Registry: registry})
	var answer struct {
		ID string `json:"id"`
	}
	if err = rpc(entry, Request{Op: "network-invite"}, bytes.NewReader(body), &answer); err != nil {
		if strings.Contains(err.Error(), "unknown RPC operation") {
			err = fmt.Errorf("its Jara cannot invite devices yet; upgrade it with jara deployment upgrade --remote %s", remoteName)
		}
		return err
	}
	code := joinToken{Name: remoteName, Host: first.Host, Port: first.Port, User: first.User, HostKeys: first.HostKeys, Secret: secret}.encode()
	fmt.Printf("On the new device, run:\n\n  jara join %s\n\n", code)
	fmt.Printf("The code works once, until %s, and gives that device full access to %d host(s) and to devices that allow calls.\n",
		expires.Format("15:04"), len(registry))
	fmt.Println("Waiting for it to join (Ctrl+C stops waiting; the code still works until it expires)...")
	for time.Now().Before(expires) {
		var joined networkJoined
		if err = rpc(entry, Request{Op: "network-joined", ID: answer.ID}, nil, &joined); err == nil && joined.Device != "" {
			return authorizeJoinedDevice(c, registry[1:], joined)
		}
		time.Sleep(2 * time.Second)
	}
	return errors.New("the code expired before a device joined")
}

// authorizeJoinedDevice lets the new device log in to the other hosts too;
// this device can, the host the new one joined through cannot.
func authorizeJoinedDevice(c Config, others []networkHost, joined networkJoined) error {
	fmt.Printf("%s joined.\n", joined.Device)
	body, _ := json.Marshal(map[string]string{joined.Device: joined.Key})
	for _, host := range others {
		if err := rpc(c.Remotes[host.Name], Request{Op: "network-authorize"}, bytes.NewReader(body), nil); err != nil {
			fmt.Printf("Could not authorize it on %s: %v\n", host.Name, err)
			continue
		}
		fmt.Printf("It can now log in to %s.\n", host.Name)
	}
	return nil
}

// joinCommand is `jara join CODE [--name NAME]`: join the network a device
// invited you to, adding its remote hosts here.
func joinCommand(c Config, args []string) error {
	f := flag.NewFlagSet("join", flag.ContinueOnError)
	name := f.String("name", runnerDeviceName(), "this device's name in the network")
	noSignIn := f.Bool("no-sign-in", false, "do not offer to sign the agent CLIs in")
	var positional []string
	for {
		if err := f.Parse(args); err != nil {
			return err
		}
		if f.NArg() == 0 {
			break
		}
		positional, args = append(positional, f.Arg(0)), f.Args()[1:]
	}
	if len(positional) != 1 || !deviceNameOK.MatchString(*name) {
		return errors.New("use jara join CODE [--name NAME] [--no-sign-in]")
	}
	token, err := decodeJoinToken(positional[0])
	if err != nil {
		return err
	}
	keyPath, publicKey, err := ensureNetworkKey(*name)
	if err != nil {
		return err
	}
	answer, err := redeemJoinToken(token, *name, publicKey)
	if err != nil {
		return err
	}
	if err = installNetwork(c, answer.Registry, keyPath, answer.Device); err != nil {
		return err
	}
	signInAfterJoin(*noSignIn)
	return nil
}

// ensureNetworkKey returns this device's key for the network, creating it.
func ensureNetworkKey(device string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(home, ".ssh", "jara_ed25519")
	if _, err = os.Stat(path); os.IsNotExist(err) {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", "", err
		}
		output, genErr := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "jara-"+device, "-f", path).CombinedOutput()
		if genErr != nil {
			return "", "", fmt.Errorf("create an SSH key: %w: %s", genErr, strings.TrimSpace(string(output)))
		}
	}
	data, err := os.ReadFile(path + ".pub")
	fields := strings.Fields(string(data))
	if err != nil || len(fields) < 2 || !peerKeyRE.MatchString(fields[0]+" "+fields[1]) {
		return "", "", fmt.Errorf("unreadable public key %s.pub", path)
	}
	return path, fields[0] + " " + fields[1], nil
}

type joinAnswer struct {
	Device   string        `json:"device"`
	Registry []networkHost `json:"registry"`
}

// redeemJoinToken logs in with the code's key, which may only run `jara
// _join`, and trades the code for this device's access and the network.
func redeemJoinToken(token joinToken, device, publicKey string) (joinAnswer, error) {
	var answer joinAnswer
	dir, err := os.MkdirTemp("", "jara-join-")
	if err != nil {
		return answer, err
	}
	defer os.RemoveAll(dir)
	keyPath, known := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	if err = os.WriteFile(keyPath, openSSHPrivateKey(joinKey(token.Secret), "jara-join"), 0600); err != nil {
		return answer, err
	}
	if err = os.WriteFile(known, []byte(knownHostLines(token.Host, token.Port, token.HostKeys)), 0600); err != nil {
		return answer, err
	}
	args := []string{"-F", "none", "-T", "-i", keyPath, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "ConnectTimeout=15"}
	if token.Port != 0 {
		args = append(args, "-p", strconv.Itoa(token.Port))
	}
	if token.User != "" {
		args = append(args, "-l", token.User)
	}
	cmd := exec.Command("ssh", append(args, "--", token.Host, "jara _join")...)
	request, _ := json.Marshal(networkJoined{Device: device, Key: publicKey})
	cmd.Stdin = bytes.NewReader(request)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err = cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if strings.Contains(detail, "Permission denied") {
			detail = "the host refused the code: it was used, it expired, or the host's Jara is too old to accept codes"
		}
		return answer, fmt.Errorf("join through %s: %s", token.Host, detail)
	}
	if err = json.Unmarshal(stdout.Bytes(), &answer); err != nil || answer.Device == "" || len(answer.Registry) == 0 {
		return answer, fmt.Errorf("the host gave an unexpected answer: %s", strings.TrimSpace(stdout.String()))
	}
	return answer, nil
}

func knownHostLines(host string, port int, keys []string) string {
	pattern := host
	if port != 0 && port != 22 {
		pattern = "[" + host + "]:" + strconv.Itoa(port)
	}
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(pattern + " " + key + "\n")
	}
	return b.String()
}

// installNetwork adds the network's hosts as remotes, pinned to their host
// keys, and checks that each lets this device in.
func installNetwork(c Config, registry []networkHost, keyPath, device string) error {
	known := filepath.Join(fsutil.DataRoot(), "known_hosts")
	var lines strings.Builder
	if data, err := os.ReadFile(known); err == nil {
		lines.Write(data)
	}
	var added []string
	for _, host := range registry {
		if !nameRE.MatchString(host.Name) || validateRemote(Remote{Host: host.Host, Port: host.Port, User: host.User}) != nil {
			fmt.Printf("Skipping an invalid host %q.\n", host.Name)
			continue
		}
		remote := Remote{Host: host.Host, Port: host.Port, User: host.User, Identity: keyPath, KnownHosts: known, Standby: host.Standby}
		if existing, ok := c.Remotes[host.Name]; ok && (existing.Host != remote.Host || existing.Port != remote.Port) {
			fmt.Printf("Keeping your remote %s; the network's host of that name is at %s.\n", host.Name, host.Host)
			continue
		}
		lines.WriteString(knownHostLines(host.Host, host.Port, host.HostKeys))
		if c.Remotes == nil {
			c.Remotes = map[string]Remote{}
		}
		if _, ok := c.Remotes[host.Name]; !ok {
			c.appendRemote(host.Name)
		}
		c.Remotes[host.Name] = remote
		if c.Default == "" {
			c.Default = host.Name
		}
		added = append(added, host.Name)
	}
	if err := os.MkdirAll(filepath.Dir(known), 0700); err != nil {
		return err
	}
	if err := portable.WriteFileAtomic(known, []byte(lines.String())); err != nil {
		return err
	}
	if err := saveConfig(c); err != nil {
		return err
	}
	fmt.Printf("Joined as %s; added %s.\n", device, strings.Join(added, ", "))
	// The inviting device authorizes this one on the other hosts right
	// after the join; give it a moment.
	for _, name := range added {
		var err error
		for attempt := 0; attempt < 15; attempt++ {
			if err = rpc(c.Remotes[name], Request{Op: "peer-host-keys"}, nil, nil); err == nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if err != nil {
			fmt.Printf("%s does not let this device in yet: %v\n", name, err)
		} else {
			fmt.Printf("%s: ready.\n", name)
		}
	}
	fmt.Println("Run jara ps to see running sessions, or jara device allow to let the other devices call this one.")
	return nil
}
