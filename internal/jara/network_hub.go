package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// A host of a Jara network keeps the invitations a device created and the
// SSH keys of the devices that joined. sshd learns both from
// `jara _peer-keys` (or a managed block in authorized_keys): an invitation's
// key may only run `jara _join`, once, before it expires; a joined device's
// key logs in like yours.

// networkHost is how a device of the network reaches one host.
type networkHost struct {
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port,omitempty"`
	User     string   `json:"user,omitempty"`
	HostKeys []string `json:"host_keys"`
	Standby  bool     `json:"standby,omitempty"`
}

type networkInvite struct {
	Key      string        `json:"key"`
	Expires  int64         `json:"expires"`
	Registry []networkHost `json:"registry"`
}

type networkJoined struct {
	Device string `json:"device"`
	Key    string `json:"key"`
}

// maxInviteLifetime bounds how long a join code works.
const maxInviteLifetime = time.Hour

func networkDir() string { return filepath.Join(fsutil.DataRoot(), "network") }

// networkAuthorizedLines renders the keys of a network directory for sshd.
func networkAuthorizedLines(dir string) []string {
	var lines []string
	invites, _ := filepath.Glob(filepath.Join(dir, "invites", "*.json"))
	sort.Strings(invites)
	for _, path := range invites {
		var invite networkInvite
		id := strings.TrimSuffix(filepath.Base(path), ".json")
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &invite) != nil || !peerKeyRE.MatchString(invite.Key) ||
			inviteID(invite.Key) != id || time.Now().Unix() > invite.Expires {
			continue
		}
		lines = append(lines, fmt.Sprintf(`restrict,command="jara _join %s" %s jara-join:%s`, id, invite.Key, id))
	}
	devices, _ := filepath.Glob(filepath.Join(dir, "devices", "*.pub"))
	sort.Strings(devices)
	for _, path := range devices {
		name := strings.TrimSuffix(filepath.Base(path), ".pub")
		data, err := os.ReadFile(path)
		key := strings.TrimSpace(string(data))
		if err == nil && deviceNameOK.MatchString(name) && peerKeyRE.MatchString(key) {
			lines = append(lines, key+" jara-device:"+name)
		}
	}
	return lines
}

// refreshManagedKeys rewrites Jara's authorized_keys block on hosts where
// sshd does not ask `jara _peer-keys`.
func refreshManagedKeys() error {
	if os.Getenv("JARA_PEER_KEYS_COMMAND") == "1" {
		return nil
	}
	return writeManagedAuthorizedKeys(append(peerAuthorizedLines(peerDir()), networkAuthorizedLines(networkDir())...))
}

// networkRPC serves the network operations of devices already in it.
func networkRPC(q Request, body io.Reader) error {
	out := json.NewEncoder(os.Stdout)
	for _, sub := range []string{"invites", "joined", "devices"} {
		if err := os.MkdirAll(filepath.Join(networkDir(), sub), 0700); err != nil {
			return err
		}
	}
	switch q.Op {
	case "network-invite":
		var invite networkInvite
		if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&invite); err != nil {
			return err
		}
		if !peerKeyRE.MatchString(invite.Key) || invite.Expires > time.Now().Add(maxInviteLifetime).Unix() || len(invite.Registry) == 0 {
			return fmt.Errorf("invalid invitation")
		}
		pruneInvites()
		id := inviteID(invite.Key)
		if err := fsutil.WriteJSON(filepath.Join(networkDir(), "invites", id+".json"), invite); err != nil {
			return err
		}
		if err := refreshManagedKeys(); err != nil {
			return err
		}
		return out.Encode(map[string]string{"id": id})
	case "network-joined":
		var joined networkJoined
		if data, err := os.ReadFile(filepath.Join(networkDir(), "joined", q.ID+".json")); err == nil && nameRE.MatchString(q.ID) {
			_ = json.Unmarshal(data, &joined)
		}
		return out.Encode(joined)
	case "network-authorize":
		var devices map[string]string
		if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&devices); err != nil {
			return err
		}
		for name, key := range devices {
			if !deviceNameOK.MatchString(name) || !peerKeyRE.MatchString(key) {
				return fmt.Errorf("invalid device %q", name)
			}
			if err := os.WriteFile(filepath.Join(networkDir(), "devices", name+".pub"), []byte(key+"\n"), 0600); err != nil {
				return err
			}
		}
		return refreshManagedKeys()
	case "network-revoke":
		if !deviceNameOK.MatchString(q.Runner) {
			return fmt.Errorf("invalid device name")
		}
		if err := os.Remove(filepath.Join(networkDir(), "devices", q.Runner+".pub")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return refreshManagedKeys()
	case "network-devices":
		paths, _ := filepath.Glob(filepath.Join(networkDir(), "devices", "*.pub"))
		names := []string{}
		for _, path := range paths {
			names = append(names, strings.TrimSuffix(filepath.Base(path), ".pub"))
		}
		sort.Strings(names)
		return out.Encode(names)
	}
	return fmt.Errorf("unknown RPC operation %s", q.Op)
}

func pruneInvites() {
	paths, _ := filepath.Glob(filepath.Join(networkDir(), "invites", "*.json"))
	for _, path := range paths {
		var invite networkInvite
		if data, err := os.ReadFile(path); err != nil || json.Unmarshal(data, &invite) != nil || time.Now().Unix() > invite.Expires {
			_ = os.Remove(path)
		}
	}
}

// runJoin is `jara _join ID`, the forced command of an invitation's key. It
// uses the invitation up, authorizes the joining device's own key and
// answers with the network: its hosts and their host keys.
func runJoin(id string, stdin io.Reader) error {
	if !nameRE.MatchString(id) {
		return fmt.Errorf("invalid invitation")
	}
	path := filepath.Join(networkDir(), "invites", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("this join code was already used or has expired")
	}
	// One use: the key stops working before anything else happens.
	if err = os.Remove(path); err != nil {
		return err
	}
	_ = refreshManagedKeys()
	var invite networkInvite
	if err = json.Unmarshal(data, &invite); err != nil || time.Now().Unix() > invite.Expires {
		return fmt.Errorf("this join code has expired")
	}
	var request networkJoined
	if err = json.NewDecoder(io.LimitReader(stdin, 64*1024)).Decode(&request); err != nil {
		return err
	}
	if !deviceNameOK.MatchString(request.Device) || !peerKeyRE.MatchString(request.Key) {
		return fmt.Errorf("invalid device name or key")
	}
	name := uniqueDeviceName(request.Device, request.Key)
	if err = os.WriteFile(filepath.Join(networkDir(), "devices", name+".pub"), []byte(request.Key+"\n"), 0600); err != nil {
		return err
	}
	if err = refreshManagedKeys(); err != nil {
		return err
	}
	joined := networkJoined{Device: name, Key: request.Key}
	if err = fsutil.WriteJSON(filepath.Join(networkDir(), "joined", id+".json"), joined); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"device": name, "registry": invite.Registry})
}

// uniqueDeviceName keeps another device's key when a joining device has
// the same name: the newcomer gets a numbered name instead.
func uniqueDeviceName(name, key string) string {
	candidate := name
	for i := 2; ; i++ {
		data, err := os.ReadFile(filepath.Join(networkDir(), "devices", candidate+".pub"))
		if err != nil || strings.TrimSpace(string(data)) == key {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", name[:min(len(name), 60)], i)
	}
}
