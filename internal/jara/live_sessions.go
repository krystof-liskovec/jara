package jara

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// liveSession is a Jara conversation session that is running somewhere:
// on this device, on a remote host, or on another device that a remote host
// reaches through its runner tunnel.
type liveSession struct {
	Workspace    string `json:"workspace"`      // the workspace ID on its remote host
	Name         string `json:"name,omitempty"` // the folder or repository name
	Runner       string `json:"runner"`         // runnerHub, or the device running the agent
	Conversation string `json:"conversation,omitempty"`
	Busy         bool   `json:"busy,omitempty"`     // a turn is running
	Attached     int    `json:"attached,omitempty"` // windows attached (host sessions only)
	// DeviceFolder is the folder on a linked device (jara device allow) of a
	// session that runs there outside any of this host's workspaces.
	DeviceFolder string `json:"device_folder,omitempty"`

	// Filled in by the client.
	Remote string `json:"-"` // the remote host that reaches it; "" for this device
	Local  string `json:"-"` // this device's session ID, when it runs here
	Folder string `json:"-"` // the local folder bound to it, if any
}

// liveSessionTimeout bounds how long `jara ps` waits for each remote host.
const liveSessionTimeout = 20 * time.Second

// hubLiveSessions is the live-sessions RPC: the remote host's own running
// sessions and those of online devices that hold one of its workspaces.
func hubLiveSessions() []liveSession {
	out := []liveSession{}
	devices := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(fsutil.DataRoot(), "runners"))
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || entry.IsDir() || !nameRE.MatchString(id) {
			continue
		}
		paths, err := runnerPathSet(id)
		if err != nil {
			continue
		}
		lease, exists, err := loadRunnerLease(paths, id)
		if err != nil || !exists || !lease.online() {
			continue
		}
		devices[id] = true
		out = append(out, liveSession{Workspace: id, Name: hubWorkspaceName(id), Runner: lease.Owner,
			Conversation: lease.Conversation, Busy: lease.Busy})
	}
	for _, session := range chatDaemonSessions() {
		if devices[session.Workspace] {
			continue // a stale host session; the device's is the live one
		}
		out = append(out, liveSession{Workspace: session.Workspace, Name: hubWorkspaceName(session.Workspace),
			Runner: runnerHub, Conversation: session.Conversation, Busy: session.AgentState == "running", Attached: session.Attached})
	}
	return append(out, deviceLiveSessions(devices)...)
}

// hubWorkspaceName names a host workspace the way its owner knows it.
func hubWorkspaceName(id string) string {
	w, err := workspaceInfo(id)
	if err != nil {
		return id
	}
	for _, name := range []string{w.Name, filepath.Base(w.SourcePath), strings.TrimSuffix(filepath.Base(w.URL), ".git")} {
		if name != "" && name != "." && name != "/" {
			return name
		}
	}
	return id
}

// localLiveSessions lists the sessions running on this device.
func localLiveSessions() []liveSession {
	var out []liveSession
	entries, _ := os.ReadDir(filepath.Join(fsutil.DataRoot(), "chat"))
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !nameRE.MatchString(id) {
			continue
		}
		state, running := readChatDaemonState(id)
		if !running {
			continue
		}
		marker := loadLocalRunnerMarker(id)
		folder := marker.Root
		if folder == "" {
			folder = state.CWD
		}
		out = append(out, liveSession{Workspace: marker.Workspace, Name: filepath.Base(folder), Runner: runnerDeviceName(),
			Conversation: state.Conversation, Busy: state.ActiveTurn, Attached: state.AttachedClients,
			Remote: marker.Remote, Local: id, Folder: folder})
	}
	return out
}

// collectLiveSessions lists running sessions on this device (unless only a
// remote is asked for) and on each remote host, in rank order. Hosts that do
// not answer are reported in problems rather than failing the listing.
func collectLiveSessions(c Config, onlyRemote string, includeLocal bool) (sessions []liveSession, problems []string) {
	if includeLocal {
		sessions = localLiveSessions()
	}
	names := c.rankedRemotes()
	if onlyRemote != "" {
		names = []string{onlyRemote}
	}
	results := make([][]liveSession, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		remote, ok := c.Remotes[name]
		if !ok {
			errs[i] = fmt.Errorf("not configured")
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			done := make(chan struct{})
			go func() {
				errs[i] = rpc(remote, Request{Op: "live-sessions"}, nil, &results[i])
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(liveSessionTimeout):
				errs[i] = fmt.Errorf("no answer within %s", liveSessionTimeout)
			}
		}()
	}
	wg.Wait()
	here := runnerDeviceName()
	seenDevice := map[string]bool{} // a device linked to several hosts is listed once
	for i, name := range names {
		if err := errs[i]; err != nil {
			if strings.Contains(err.Error(), "unknown RPC operation") {
				err = fmt.Errorf("its Jara cannot list sessions yet; upgrade it with jara deployment upgrade --remote %s", name)
			}
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for _, session := range results[i] {
			session.Remote = name
			if session.Runner == here && (session.DeviceFolder != "" || runsLocally(sessions, name, session.Workspace)) {
				continue // listed above as this device's own session
			}
			if session.DeviceFolder != "" {
				key := session.Runner + "\x00" + session.DeviceFolder
				if seenDevice[key] {
					continue
				}
				seenDevice[key] = true
			}
			for folder, binding := range c.Bindings {
				if binding.Remote == name && binding.Workspace == session.Workspace {
					session.Folder = folder
				}
			}
			sessions = append(sessions, session)
		}
	}
	return sessions, problems
}

func runsLocally(sessions []liveSession, remote, workspace string) bool {
	for _, session := range sessions {
		if session.Local != "" && session.Remote == remote && session.Workspace == workspace {
			return true
		}
	}
	return false
}

// where says where a session runs, as `jara ps` shows it.
func (s liveSession) where() string {
	switch {
	case s.Local != "":
		return "this device"
	case s.Runner == runnerHub:
		return s.Remote
	default:
		return s.Runner + " (via " + s.Remote + ")"
	}
}

// target is how `jara attach` names the session.
func (s liveSession) target() string {
	if s.Local != "" {
		return s.Local
	}
	if s.DeviceFolder != "" {
		return s.Runner + ":" + s.DeviceFolder
	}
	return s.Workspace
}

// printLiveSessions writes the numbered table `jara attach N` refers to.
func printLiveSessions(sessions []liveSession, problems []string) {
	if len(sessions) == 0 {
		fmt.Println("No Jara sessions are running.")
	}
	rows := [][]string{{"#", "WORKSPACE", "RUNS ON", "STATE", "WINDOWS", "ID", "FOLDER"}}
	for i, s := range sessions {
		state := "idle"
		if s.Busy {
			state = "working"
		}
		windows := fmt.Sprint(s.Attached)
		if s.Local == "" && s.Runner != runnerHub && s.DeviceFolder == "" {
			windows = "-" // the host only knows the device is online
		}
		rows = append(rows, []string{fmt.Sprint(i + 1), s.Name, s.where(), state, windows, s.target(), displayHomePath(s.Folder)})
	}
	if len(rows) > 1 {
		widths := make([]int, len(rows[0]))
		for _, row := range rows {
			for i, cell := range row {
				widths[i] = max(widths[i], len(cell))
			}
		}
		for _, row := range rows {
			var b strings.Builder
			for i, cell := range row {
				if i == len(row)-1 {
					b.WriteString(cell)
				} else {
					b.WriteString(cell + strings.Repeat(" ", widths[i]-len(cell)+2))
				}
			}
			fmt.Println(strings.TrimRight(b.String(), " "))
		}
	}
	sort.Strings(problems)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, "Could not list sessions on", problem)
	}
}

func displayHomePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && pathWithin(home, path) {
		if rel, relErr := filepath.Rel(home, path); relErr == nil {
			return filepath.Join("~", rel)
		}
	}
	return path
}
