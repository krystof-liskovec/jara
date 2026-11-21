package jara

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

// Devices that allow it (jara device allow) keep one link to each of their
// remote hosts, like a runner link: an outbound SSH connection that forwards
// the device's daemon socket to the host. Devices need no SSH server; any
// client of the host reaches them through it, to list their folders and to
// start or join sessions there.

var deviceNameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// deviceRecord is the host's view of a linked device.
type deviceRecord struct {
	Name     string `json:"name"`
	Link     string `json:"link"`
	LastSeen int64  `json:"last_seen"`
}

func (d deviceRecord) online() bool {
	return d.LastSeen > 0 && time.Since(time.Unix(d.LastSeen, 0)) < runnerLinkTimeout()
}

func deviceHubDir() string { return filepath.Join(fsutil.DataRoot(), "devices") }

func deviceRecordPath(name string) string { return filepath.Join(deviceHubDir(), name+".json") }

// deviceSocketPath is short: Unix socket paths are limited to ~100 bytes.
func deviceSocketPath(name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(deviceHubDir(), hex.EncodeToString(sum[:6])+".sock")
}

func loadDeviceRecord(name string) (deviceRecord, bool) {
	var record deviceRecord
	data, err := os.ReadFile(deviceRecordPath(name))
	if err != nil || json.Unmarshal(data, &record) != nil || record.Name != name {
		return deviceRecord{}, false
	}
	return record, true
}

// deviceFolder is a folder a device has opened with Jara, as it reports it.
type deviceFolder struct {
	Folder       string `json:"folder"`
	Remote       string `json:"remote,omitempty"`    // the device's name for the remote it is attached to
	Workspace    string `json:"workspace,omitempty"` // its workspace there
	Running      bool   `json:"running,omitempty"`
	Busy         bool   `json:"busy,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Attached     int    `json:"attached,omitempty"`
}

// deviceInfo is one device in the device-list answer.
type deviceInfo struct {
	Name    string         `json:"name"`
	Folders []deviceFolder `json:"folders,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// deviceRequest is what the host asks a device's daemon.
type deviceRequest struct {
	Op           string `json:"op"` // folders, open
	Folder       string `json:"folder,omitempty"`
	Conversation string `json:"conversation,omitempty"`
}

// deviceRPC serves device-tunnel (a device starting its link) and
// device-list (a client asking which devices it can reach).
func deviceRPC(q Request) error {
	out := json.NewEncoder(os.Stdout)
	switch q.Op {
	case "device-tunnel":
		if !deviceNameOK.MatchString(q.Runner) {
			return fmt.Errorf("invalid device name")
		}
		if err := os.MkdirAll(deviceHubDir(), 0700); err != nil {
			return err
		}
		// Only this device's link uses the socket; one that exists is left
		// from a dropped connection, which sshd may not replace.
		socket := deviceSocketPath(q.Runner)
		_ = os.Remove(socket)
		return out.Encode(map[string]string{"socket": socket})
	case "device-list":
		return out.Encode(hubDevices())
	}
	return fmt.Errorf("unknown RPC operation %s", q.Op)
}

// runDeviceLink is `jara _device-link` on the host: the command of a
// device's link connection. Each heartbeat line renews the device's record;
// a newer link of the same device replaces this one.
func runDeviceLink(q Request) error {
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	if !deviceNameOK.MatchString(q.Runner) || q.Link == "" {
		return fmt.Errorf("invalid device link")
	}
	if err := os.MkdirAll(deviceHubDir(), 0700); err != nil {
		return err
	}
	record := deviceRecord{Name: q.Runner, Link: q.Link, LastSeen: time.Now().Unix()}
	if err := fsutil.WriteJSON(deviceRecordPath(q.Runner), record); err != nil {
		return err
	}
	scanner := protocol.NewLineScanner(os.Stdin)
	for scanner.Scan() {
		if current, ok := loadDeviceRecord(q.Runner); ok && current.Link != q.Link {
			return fmt.Errorf("a newer link of %s replaced this one", q.Runner)
		}
		record.LastSeen = time.Now().Unix()
		if err := fsutil.WriteJSON(deviceRecordPath(q.Runner), record); err != nil {
			return err
		}
	}
	if current, ok := loadDeviceRecord(q.Runner); ok && current.Link == q.Link {
		current.LastSeen = 0 // gone now, not after the link timeout
		_ = fsutil.WriteJSON(deviceRecordPath(q.Runner), current)
	}
	return nil
}

// dialDevice connects to an online device's daemon through its link.
func dialDevice(name string) (net.Conn, error) {
	if !deviceNameOK.MatchString(name) {
		return nil, fmt.Errorf("invalid device name")
	}
	record, ok := loadDeviceRecord(name)
	if !ok || !record.online() {
		return nil, fmt.Errorf("device %s is not linked to this host; on it, run jara device allow (with the Jara daemon running)", name)
	}
	conn, err := net.DialTimeout("unix", deviceSocketPath(name), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("device %s is reconnecting; try again shortly", name)
	}
	return conn, nil
}

// askDevice sends one request to a device and decodes its one-line answer.
func askDevice(name string, request deviceRequest, answer any) error {
	conn, err := dialDevice(name)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err = json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	line, err := bufio.NewReaderSize(conn, 64*1024).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("device %s did not answer: %w", name, err)
	}
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(line, &failure) == nil && failure.Error != "" {
		return fmt.Errorf("device %s: %s", name, failure.Error)
	}
	return json.Unmarshal(line, answer)
}

// hubDevices lists the online devices and the folders each has opened.
func hubDevices() []deviceInfo {
	entries, _ := os.ReadDir(deviceHubDir())
	var names []string
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok {
			if record, found := loadDeviceRecord(name); found && record.online() {
				names = append(names, name)
			}
		}
	}
	devices := make([]deviceInfo, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			devices[i].Name = name
			if err := askDevice(name, deviceRequest{Op: "folders"}, &devices[i].Folders); err != nil {
				devices[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	return devices
}

// runDeviceOpen is `jara _device-open` on the host: it asks a device to
// start or join the session of one of its folders and relays the session.
func runDeviceOpen(q Request) error {
	conn, err := dialDevice(q.Runner)
	if err != nil {
		return sendChatError(err)
	}
	if err = json.NewEncoder(conn).Encode(deviceRequest{Op: "open", Folder: q.Root, Conversation: q.Conversation}); err != nil {
		conn.Close()
		return sendChatError(err)
	}
	return proxyChatDaemon(conn)
}

func sendChatError(err error) error {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"method": "jara/error", "params": map[string]string{"message": err.Error()}})
	return err
}

// deviceLiveSessions adds the running sessions of linked devices to the
// host's live-sessions answer, except those already listed through the
// workspaces of this host they run.
func deviceLiveSessions(listed map[string]bool) []liveSession {
	var out []liveSession
	for _, device := range hubDevices() {
		for _, folder := range device.Folders {
			if !folder.Running || folder.Workspace != "" && listed[folder.Workspace] {
				continue
			}
			out = append(out, liveSession{Name: filepath.Base(folder.Folder), Runner: device.Name, Busy: folder.Busy,
				Conversation: folder.Conversation, Attached: folder.Attached, DeviceFolder: folder.Folder})
		}
	}
	return out
}
