package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
)

// localRunnerMarker remembers, on this device, which checkpoint its checkout
// and conversations reflect and which conversation versions it has saved.
type localRunnerMarker struct {
	Remote    string            `json:"remote"`
	Workspace string            `json:"workspace"`
	Root      string            `json:"root"`
	Seq       int64             `json:"seq"`
	Epoch     int64             `json:"epoch"`
	Versions  map[string]string `json:"versions,omitempty"`
	Lost      bool              `json:"lost,omitempty"`
	// Branch is the branch both checkouts shared at the last sync.
	Branch string `json:"branch,omitempty"`
	// Sent is what the host stores of each local session file.
	Sent map[string]runnerSentFile `json:"sent,omitempty"`
}

var deviceNameRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

var errRunnerUnsupported = errors.New("the remote Jara cannot hand workspaces to devices yet; upgrade it with `jara deployment upgrade`")

// runnerDeviceName names this device in leases; JARA_DEVICE overrides it.
func runnerDeviceName() string {
	name := os.Getenv("JARA_DEVICE")
	if name == "" {
		name, _ = os.Hostname()
	}
	name = strings.Trim(deviceNameRE.ReplaceAllString(name, "-"), "-")
	if name == "" {
		name = "this-device"
	}
	return name[:min(len(name), 64)]
}

// localRunnerID names this device's session for a remote workspace.
func localRunnerID(remoteName, workspace string) string {
	sum := sha256.Sum256([]byte(remoteName + "\x00" + workspace))
	return "local-" + hex.EncodeToString(sum[:6])
}

func localRunnerMarkerPath(id string) string {
	return filepath.Join(fsutil.DataRoot(), "runner-local", id+".json")
}

func loadLocalRunnerMarker(id string) localRunnerMarker {
	var marker localRunnerMarker
	if data, err := os.ReadFile(localRunnerMarkerPath(id)); err == nil {
		_ = json.Unmarshal(data, &marker)
	}
	return marker
}

func updateLocalRunnerMarker(id string, change func(*localRunnerMarker)) error {
	marker := loadLocalRunnerMarker(id)
	change(&marker)
	return fsutil.WriteJSON(localRunnerMarkerPath(id), marker)
}

type localRunnerOpen struct {
	remote         Remote
	remoteName     string
	binding        Binding
	root           string
	isGit          bool
	includeIgnored bool
	takeOver       bool
}

// openLocalRunner opens a workspace's conversation with its agent running on
// this device. The window follows the conversation: while another machine
// runs it (the host continuing work this device left when it slept, or
// another device), the window watches that session, and the conversation
// moves back here as soon as that machine is idle.
// Without a remote (o.remoteName empty) the session simply runs here.
func openLocalRunner(o localRunnerOpen, q Request) error {
	id, err := localSessionID(o.root, o.remoteName, o.binding.Workspace)
	if err != nil {
		return err
	}
	q.UserInterface, q.ID, q.Root, q.RemoteName, q.Workspace, q.Runner =
		"jara", id, o.root, o.remoteName, o.binding.Workspace, runnerDeviceName()
	chatPlace = chatPlacement{root: o.root, session: id} // the agent runs here
	connection, ready, err := connectLocalRunner(o, q, true)
	if err != nil {
		return err
	}
	conversation := q.Conversation
	if conversation == "" && !q.New && !q.Resume && !q.ResumeLatest {
		conversation = ready.Conversation
	}
	options := chatLoopOptions{
		cwd: ready.CWD, id: conversation, workspaceLabel: ready.Workspace,
		persistentSession: true, initialized: ready.Initialized, attachedTurnActive: ready.ActiveTurn,
		resumeAtStart: q.Conversation == "" && (q.Resume || q.ResumeLatest),
		resumeLatest:  q.Conversation == "" && q.ResumeLatest,
		reconnect: func(thread string) (*appConnection, remoteChatServerReady, error) {
			again := q
			again.Conversation, again.New, again.Resume, again.ResumeLatest = thread, false, false, false
			return connectLocalRunner(o, again, false)
		},
		notice: ready.Notice,
	}
	return chatLoopWithOptions(connection, os.Stdin, os.Stdout, options)
}

// connectLocalRunner connects to this device's session, starting it after
// claiming the workspace, or to the machine the claim has to wait for.
func connectLocalRunner(o localRunnerOpen, q Request, first bool) (*appConnection, remoteChatServerReady, error) {
	marker := loadLocalRunnerMarker(q.ID)
	if chatDaemonRunning(q.ID) {
		if o.remoteName != "" && marker.Remote == "" {
			return nil, remoteChatServerReady{}, fmt.Errorf("this folder's session is running without a remote; end it with /quit, then open it again to attach it to %s", o.remoteName)
		}
		conn, err := connectExistingChatDaemon(q.ID)
		if err != nil {
			return nil, remoteChatServerReady{}, err
		}
		notice := ""
		if o.remoteName != "" {
			notice = "The agent runs on this device."
		}
		return readyLocalRunner(conn, notice)
	}
	if o.remoteName == "" {
		return startLocalOnlyRunner(q)
	}
	var claim runnerClaim
	// A window reconnecting by itself only reclaims the conversation from the
	// host; it never displaces another device the user moved to.
	wire := Request{Op: "runner-claim", ID: o.binding.Workspace, Runner: q.Runner, Force: first && o.takeOver, Wait: true, Passive: !first, Since: marker.Seq}
	if err := rpc(o.remote, wire, nil, &claim); err != nil {
		if strings.Contains(err.Error(), "unknown RPC operation") {
			err = errRunnerUnsupported
		}
		return nil, remoteChatServerReady{}, err
	}
	if claim.Wait {
		return watchRunner(o, q, claim.Runner)
	}
	q.Epoch = claim.Epoch
	release := func() {
		_ = rpc(o.remote, Request{Op: "runner-release", ID: o.binding.Workspace, Runner: q.Runner, Epoch: claim.Epoch}, nil, nil)
	}
	if claim.Seq != marker.Seq {
		if err := catchUpLocalRunner(o, &q, marker.Seq, claim); err != nil {
			release()
			return nil, remoteChatServerReady{}, err
		}
	} else if err := pushBeforeRunning(o, marker.Branch); err != nil {
		release()
		return nil, remoteChatServerReady{}, fmt.Errorf("copy local changes to the remote before starting: %w", err)
	}
	if q.Conversation == "" && !q.New && !q.Resume && !q.ResumeLatest {
		q.Conversation = claim.Conversation
		if paths, err := chatDaemonPathSet(q.ID); err == nil {
			if previous, ok, _ := readSavedChatDaemonState(q.ID, paths); ok && previous.Conversation != "" {
				q.Conversation = previous.Conversation
			}
		}
	}
	err := updateLocalRunnerMarker(q.ID, func(m *localRunnerMarker) {
		m.Remote, m.Workspace, m.Root, m.Seq, m.Epoch, m.Lost = o.remoteName, o.binding.Workspace, o.root, claim.Seq, claim.Epoch, false
		m.Branch = checkoutState(o.root).Branch
		// Conversations the host has never received are sent by the first
		// checkpoint: attaching a folder keeps its local history.
		if m.Versions == nil {
			m.Versions = map[string]string{}
		}
	})
	if err != nil {
		release()
		return nil, remoteChatServerReady{}, err
	}
	conn, err := startLocalRunnerDaemon(q)
	if err != nil {
		release()
		return nil, remoteChatServerReady{}, err
	}
	return readyLocalRunner(conn, fmt.Sprintf("The agent runs on this device (%s); %s keeps every conversation after each turn.", q.Runner, o.remoteName))
}

func readyLocalRunner(conn net.Conn, notice string) (*appConnection, remoteChatServerReady, error) {
	app := startChatConnection(conn)
	ready, err := awaitRemoteChatReady(app)
	if err != nil {
		app.close()
		return nil, ready, err
	}
	if notice != "" {
		ready.Notice = notice
	}
	return app, ready, nil
}

// watchRunner attaches this window to the session of the machine running the
// conversation, through the remote host. When that machine hands over, the
// session ends and the window reconnects, now claiming it here.
func watchRunner(o localRunnerOpen, q Request, runner string) (*appConnection, remoteChatServerReady, error) {
	wire := Request{UserInterface: "jara", ID: o.binding.Workspace, Conversation: q.Conversation, Reconnect: true}
	diagnostics := &chatDiagnosticBuffer{}
	connection, err := startChatServerCommand(sshCommand(o.remote, false, shellArgs("jara", "_chat-server", token(wire))), diagnostics)
	if err != nil {
		return nil, remoteChatServerReady{}, err
	}
	ready, err := awaitRemoteChatReady(connection)
	if err != nil {
		connection.close()
		if detail := strings.TrimSpace(diagnostics.String()); detail != "" {
			err = fmt.Errorf("%w: %s", err, chatText(detail))
		}
		return nil, ready, err
	}
	ready.Notice = fmt.Sprintf("%s is running this conversation; you are watching it live. It moves to this device when %s is idle.", runner, runner)
	if runner != runnerHub {
		ready.Notice = fmt.Sprintf("%s is running this conversation; you are watching it live. Run jara --local to move it here.", runner)
	}
	return connection, ready, nil
}

// catchUpLocalRunner brings the checkout, conversations and scheduled prompts
// to the newest checkpoint, which another machine wrote.
func catchUpLocalRunner(o localRunnerOpen, q *Request, since int64, claim runnerClaim) error {
	// Work this device did after its last checkpoint, before another machine
	// replaced it, is set aside. Edits made here without running the agent
	// merge like an ordinary pull, which refuses conflicting changes.
	marker := loadLocalRunnerMarker(q.ID)
	displaced := marker.Lost
	if displaced {
		if err := setAsideLocalWork(o, q.ID); err != nil {
			return err
		}
	}
	moved, err := alignRunnerBranch(o, marker.Branch, false, false)
	if err != nil {
		return fmt.Errorf("bring this checkout up to date: %w", err)
	}
	// A clean Git checkout has nothing to lose, even without sync history.
	clean := false
	if o.isGit {
		status, statusErr := gitOutput(o.root, "status", "--porcelain")
		clean = statusErr == nil && status == ""
	}
	direction, force := "pull", displaced || clean
	if moved == runnerBranchHost {
		// The host followed this checkout's branch and set its own changes
		// aside, so this checkout is the newer copy of that branch.
		direction, force = "push", true
	}
	if err = syncWorkspace(o.remote, o.binding, o.root, direction, force, o.includeIgnored, true); err != nil {
		return fmt.Errorf("bring this checkout up to date: %w", err)
	}
	checkpoint, briefed, err := fetchCheckpoint(o, since, claim.Root, claim.Changed)
	if err != nil {
		return err
	}
	if len(briefed) > 0 {
		fmt.Fprintf(os.Stderr, "%d conversation(s) continue from Jara's transcript; their agent sessions could not be restored here.\n", len(briefed))
	}
	if paths, pathErr := chatDaemonPathSet(q.ID); pathErr == nil {
		if err = replaceSchedules(paths.schedules, checkpoint.Schedules); err != nil {
			return err
		}
		if err = installCrunch(paths.crunch, checkpoint.Crunch); err != nil {
			return err
		}
	}
	if q.Conversation == "" && !q.New {
		q.Conversation, q.Resume, q.ResumeLatest = checkpoint.Current, false, false
	}
	// The host converts its copies to this checkout's form for the next
	// checkpoint's deltas.
	return updateLocalRunnerMarker(q.ID, func(m *localRunnerMarker) {
		m.Versions, m.Sent = versionsAfterCatchUp(o.root, m.Versions, checkpoint), sentAfterImport(m.Sent, checkpoint)
	})
}

// defaultLocalRunner reports whether a workspace attached to a remote runs
// its agent on this device unless --run-remote says otherwise: the agent CLI
// is installed here, and JARA_RUN_REMOTE=1 does not opt out.
func defaultLocalRunner() bool {
	if os.Getenv("JARA_RUN_REMOTE") == "1" || runtime.GOOS == "windows" {
		return false
	}
	_, err := exec.LookPath("codex")
	return err == nil
}

// versionsAfterCatchUp records the conversations the host already has after
// a catch-up: the ones it sent and the ones this device sent before.
// Conversations only this device has stay unrecorded, so the next
// checkpoint sends them.
func versionsAfterCatchUp(root string, known map[string]string, checkpoint runnerCheckpoint) map[string]string {
	imported := map[string]bool{}
	for _, bundle := range checkpoint.Threads {
		imported[bundle.ThreadID] = true
	}
	versions := map[string]string{}
	for id, version := range workspaceThreadVersions(root) {
		if _, sent := known[id]; sent || imported[id] {
			versions[id] = version
		}
	}
	return versions
}
