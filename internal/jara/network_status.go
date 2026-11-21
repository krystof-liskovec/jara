package jara

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// networkStatus is one host's answer to network-status: its Jara version,
// every device it knows (linked now or before, or joined with a code), and
// the sessions it can reach.
type networkStatus struct {
	Version  string          `json:"version"`
	Devices  []networkDevice `json:"devices"`
	Sessions []liveSession   `json:"sessions"`
}

// networkDevice is a device as one host knows it.
type networkDevice struct {
	Name     string `json:"name"`
	Online   bool   `json:"online,omitempty"` // linked now: it accepts calls
	LastSeen int64  `json:"last_seen,omitempty"`
	Joined   bool   `json:"joined,omitempty"` // logs in with a key from a join code
	Folders  int    `json:"folders,omitempty"`
	Running  int    `json:"running,omitempty"`
	Error    string `json:"error,omitempty"`
}

// hubNetworkStatus is the network-status RPC.
func hubNetworkStatus() networkStatus {
	linked := hubDevices()
	devices := map[string]*networkDevice{}
	device := func(name string) *networkDevice {
		if devices[name] == nil {
			devices[name] = &networkDevice{Name: name}
		}
		return devices[name]
	}
	entries, _ := os.ReadDir(deviceHubDir())
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok {
			if record, found := loadDeviceRecord(name); found {
				device(name).LastSeen = record.LastSeen
			}
		}
	}
	keys, _ := filepath.Glob(filepath.Join(networkDir(), "devices", "*.pub"))
	for _, path := range keys {
		if name := strings.TrimSuffix(filepath.Base(path), ".pub"); deviceNameOK.MatchString(name) {
			device(name).Joined = true
		}
	}
	for _, info := range linked {
		d := device(info.Name)
		d.Online, d.Folders, d.Error = true, len(info.Folders), info.Error
		for _, folder := range info.Folders {
			if folder.Running {
				d.Running++
			}
		}
	}
	status := networkStatus{Version: Version, Devices: []networkDevice{}, Sessions: hubLiveSessionsWith(linked)}
	for _, d := range devices {
		status.Devices = append(status.Devices, *d)
	}
	sort.Slice(status.Devices, func(i, j int) bool { return status.Devices[i].Name < status.Devices[j].Name })
	return status
}

func networkStatusRPC() error { return json.NewEncoder(os.Stdout).Encode(hubNetworkStatus()) }

// networkView is the whole network as `jara network` shows it.
type networkView struct {
	Here     string
	Hosts    []networkHostView
	Devices  []networkDeviceView
	Sessions []liveSession
}

type networkHostView struct {
	Name, Address, Version, Role, Problem string
	Online, Outdated                      bool
}

type networkDeviceView struct {
	Name     string
	This     bool
	Callable bool     // this device: allowed calls; others: linked now
	Daemon   bool     // this device: its daemon runs
	Via      []string // hosts it is linked to now
	Joined   bool
	LastSeen int64
	Folders  int
	Running  int
	Problem  string
}

// collectNetwork asks every host at once and merges what they know.
func collectNetwork(c Config) networkView {
	view := networkView{Here: runnerDeviceName()}
	names := c.rankedRemotes()
	statuses, errs := askEachHost[networkStatus](c, names, "network-status")
	sessions := make([][]liveSession, len(names))
	devices := map[string]*networkDeviceView{}
	for i, name := range names {
		remote := c.Remotes[name]
		host := networkHostView{Name: name, Address: remote.Host, Online: errs[i] == nil, Version: statuses[i].Version}
		if remote.Port != 0 && remote.Port != 22 {
			host.Address += ":" + itoa(remote.Port)
		}
		switch {
		case i == 0:
			host.Role = "primary"
		case remote.Standby:
			host.Role = "standby"
		}
		if err := errs[i]; err != nil {
			if strings.Contains(err.Error(), "unknown RPC operation") {
				host.Online, host.Outdated = true, true
				host.Problem = "needs jara deployment upgrade --remote " + name
			} else {
				host.Problem = firstLine(err.Error())
			}
		}
		view.Hosts = append(view.Hosts, host)
		sessions[i] = statuses[i].Sessions
		for _, d := range statuses[i].Devices {
			merged := devices[d.Name]
			if merged == nil {
				merged = &networkDeviceView{Name: d.Name}
				devices[d.Name] = merged
			}
			merged.Joined = merged.Joined || d.Joined
			merged.LastSeen = max(merged.LastSeen, d.LastSeen)
			if d.Online {
				merged.Callable = true
				merged.Via = append(merged.Via, name)
				merged.Folders, merged.Running = max(merged.Folders, d.Folders), max(merged.Running, d.Running)
				if d.Error != "" {
					merged.Problem = d.Error
				}
			}
		}
	}
	local := localLiveSessions()
	here := networkDeviceView{Name: view.Here, This: true, Callable: c.DeviceCallable, Folders: len(deviceFolders(c)), Running: len(local)}
	if known := devices[view.Here]; known != nil {
		here.Via, here.Joined = known.Via, known.Joined
		delete(devices, view.Here)
	}
	_, daemonErr := callDaemon(daemonMessage{Op: "ping"})
	here.Daemon = daemonErr == nil
	view.Devices = append(view.Devices, here)
	others := make([]networkDeviceView, 0, len(devices))
	for _, d := range devices {
		others = append(others, *d)
	}
	sort.Slice(others, func(i, j int) bool {
		if others[i].Callable != others[j].Callable {
			return others[i].Callable
		}
		return others[i].Name < others[j].Name
	})
	view.Devices = append(view.Devices, others...)
	view.Sessions = mergeLiveSessions(c, names, sessions, errs, local)
	return view
}

// ago says how long ago unix was, in the largest whole unit.
func ago(unix int64, now time.Time) string {
	since := now.Sub(time.Unix(unix, 0))
	switch {
	case since < time.Minute:
		return "just now"
	case since < time.Hour:
		return itoa(int(since.Minutes())) + " min ago"
	case since < 48*time.Hour:
		return itoa(int(since.Hours())) + " h ago"
	}
	return itoa(int(since.Hours()/24)) + " days ago"
}
