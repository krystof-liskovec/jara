package jara

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// The Jara daemon serves its linked remote hosts on a second socket that
// only answers device requests: listing the folders this device has opened
// with Jara, and starting or joining the session of one of them. Its own
// control socket (which starts arbitrary sessions for local clients) is
// never forwarded.

func deviceRemoteSocketPath() string { return filepath.Join(daemonDir(), "remote.sock") }

func deviceEnvPath() string { return filepath.Join(daemonDir(), "env.json") }

// saveDeviceEnvironment remembers the environment of a Jara command run in
// your shell. Sessions other devices start here use it, so they find the
// same agent CLIs, credentials and SSH agent as sessions you start.
func saveDeviceEnvironment() {
	if err := os.MkdirAll(daemonDir(), 0700); err == nil {
		_ = fsutil.WriteJSON(deviceEnvPath(), os.Environ())
	}
}

// adoptDeviceEnvironment makes the daemon run with the saved environment.
func adoptDeviceEnvironment() {
	data, err := os.ReadFile(deviceEnvPath())
	if err != nil {
		return
	}
	var env []string
	if json.Unmarshal(data, &env) != nil {
		return
	}
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok && key != "" && !strings.HasPrefix(key, "JARA_DATA") {
			_ = os.Setenv(key, value)
		}
	}
}

// serveDeviceRemote accepts the requests linked hosts relay to this device
// until stop is closed.
func serveDeviceRemote(stop <-chan struct{}) error {
	path := deviceRemoteSocketPath()
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return err
	}
	go func() { <-stop; listener.Close() }()
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			_ = os.Remove(path)
			return nil
		}
		go handleDeviceRequest(conn)
	}
}

func handleDeviceRequest(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReaderSize(conn, 64*1024)
	line, err := reader.ReadBytes('\n')
	var request deviceRequest
	if err != nil || json.Unmarshal(line, &request) != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	c, err := readConfig()
	if err == nil && !c.DeviceCallable {
		err = errors.New("this device does not accept sessions from other devices; run jara device allow on it")
	}
	if err != nil {
		_ = json.NewEncoder(conn).Encode(map[string]string{"error": err.Error()})
		return
	}
	switch request.Op {
	case "folders":
		_ = json.NewEncoder(conn).Encode(deviceFolders(c))
	case "open":
		session, ready, openErr := openDeviceFolder(c, request)
		if openErr != nil {
			fmt.Fprintf(os.Stderr, "A remote request to open %s failed: %v\n", request.Folder, openErr)
			_ = json.NewEncoder(conn).Encode(map[string]any{"method": "jara/error", "params": map[string]string{"message": openErr.Error()}})
			return
		}
		defer session.Close()
		if _, err = conn.Write(ready); err != nil {
			return
		}
		spliceDeviceSession(conn, reader, session)
	default:
		_ = json.NewEncoder(conn).Encode(map[string]string{"error": "unknown device operation " + request.Op})
	}
}

// deviceFolders lists the folders this device has opened with Jara: those
// with a session record, and those attached to a remote.
func deviceFolders(c Config) []deviceFolder {
	roots := map[string]bool{}
	markers, _ := filepath.Glob(filepath.Join(fsutil.DataRoot(), "runner-local", "*.json"))
	for _, path := range markers {
		if root := loadLocalRunnerMarker(strings.TrimSuffix(filepath.Base(path), ".json")).Root; root != "" {
			roots[root] = true
		}
	}
	for root := range c.Bindings {
		roots[root] = true
	}
	var folders []deviceFolder
	for root := range roots {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		folder := deviceFolder{Folder: root}
		if binding, ok := c.Bindings[root]; ok {
			folder.Remote, folder.Workspace = binding.Remote, binding.Workspace
		}
		if state, running := readChatDaemonState(localWorkspaceID(root)); running {
			folder.Running, folder.Busy, folder.Conversation, folder.Attached = true, state.ActiveTurn, state.Conversation, state.AttachedClients
		}
		folders = append(folders, folder)
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Folder < folders[j].Folder })
	return folders
}

// openDeviceFolder starts the folder's session here, exactly as jara in the
// folder would (claiming it from its remote host when attached), and
// connects to it. It returns the session's ready line, already read.
func openDeviceFolder(c Config, request deviceRequest) (net.Conn, []byte, error) {
	known := false
	for _, folder := range deviceFolders(c) {
		known = known || folder.Folder == request.Folder
	}
	if !known {
		return nil, nil, fmt.Errorf("%s is not a folder this device has opened with Jara", request.Folder)
	}
	if !localRunnerAvailable() {
		return nil, nil, errors.New("the Codex CLI is not installed on this device (or not on the PATH Jara saved; run jara device allow there again)")
	}
	root := request.Folder
	o := localRunnerOpen{root: root}
	if binding, attached := c.Bindings[root]; attached {
		remote, ok := c.Remotes[binding.Remote]
		if !ok {
			return nil, nil, fmt.Errorf("the folder's remote %s is not configured on this device", binding.Remote)
		}
		_, _, isGit := workspaceContext(c, root, false)
		o = localRunnerOpen{remote: remote, remoteName: binding.Remote, binding: binding, root: root, isGit: isGit}
	}
	agent, err := resolveClientAgent(c, root, o.remoteName, "open", "")
	if err != nil {
		return nil, nil, err
	}
	q, err := localRunnerRequest(o, Request{Runtime: agent, Conversation: request.Conversation})
	if err != nil {
		return nil, nil, err
	}
	app, ready, err := connectLocalRunner(o, q, true)
	if err != nil {
		return nil, nil, err
	}
	defer app.close()
	if ready.Runner != chatHereRunner {
		return nil, nil, fmt.Errorf("%s runs this conversation; attach to it there (jara ps)", ready.Runner)
	}
	// A second connection carries the relayed session. The first closes
	// only once the session accepted this one, so it never looks deserted.
	session, err := connectExistingChatDaemon(q.ID)
	if err != nil {
		return nil, nil, err
	}
	reader := bufio.NewReaderSize(session, 64*1024)
	_ = session.SetReadDeadline(time.Now().Add(time.Minute))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		session.Close()
		return nil, nil, fmt.Errorf("the session did not answer: %w", err)
	}
	_ = session.SetReadDeadline(time.Time{})
	return &bufferedConn{Conn: session, reader: reader}, line, nil
}

// bufferedConn reads what a bufio.Reader already took from Conn first.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.reader.Read(p) }

// spliceDeviceSession relays between the host's connection (with what its
// reader already buffered) and the session until either side ends.
func spliceDeviceSession(host net.Conn, hostReader *bufio.Reader, session net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(session, hostReader); done <- struct{}{} }()
	go func() { _, _ = io.Copy(host, session); done <- struct{}{} }()
	<-done
}
