package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

// localRunnerMarker remembers, on this device, which checkpoint its checkout
// and agent state reflect, so it can tell whether another device continued
// the conversation in the meantime.
type localRunnerMarker struct {
	Remote    string `json:"remote"`
	Workspace string `json:"workspace"`
	Root      string `json:"root"`
	Seq       int64  `json:"seq"`
	Epoch     int64  `json:"epoch"`
	Lost      bool   `json:"lost,omitempty"`
}

var deviceNameRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

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
	if len(name) > 64 {
		name = name[:64]
	}
	return name
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

// openLocalRunner runs a workspace's agent on this device. It claims the
// workspace from the remote host, catches up with a checkpoint written
// elsewhere (setting aside local work it would overwrite), and starts or
// reattaches to the local session.
func openLocalRunner(o localRunnerOpen, q Request) error {
	device := runnerDeviceName()
	id := localRunnerID(o.remoteName, o.binding.Workspace)
	q.UserInterface, q.ID, q.Root, q.RemoteName, q.Workspace, q.Runner = "jara", id, o.root, o.remoteName, o.binding.Workspace, device
	if chatDaemonRunning(id) {
		conn, err := connectExistingChatDaemon(id)
		if err != nil {
			return err
		}
		return finishLocalRunnerUI(id, runRemoteChatClientConnection(q, conn))
	}
	var claim runnerClaim
	err := rpc(o.remote, Request{Op: "runner-claim", ID: o.binding.Workspace, Runner: device, Force: o.takeOver}, nil, &claim)
	if err != nil {
		if strings.Contains(err.Error(), "unknown RPC operation") {
			return fmt.Errorf("the remote Jara cannot hand workspaces to devices yet; upgrade it with `jara deployment upgrade`")
		}
		return err
	}
	q.Epoch = claim.Epoch
	release := func() {
		_ = rpc(o.remote, Request{Op: "runner-release", ID: o.binding.Workspace, Runner: device, Epoch: claim.Epoch}, nil, nil)
	}
	marker := loadLocalRunnerMarker(id)
	if claim.Seq > 0 && claim.Seq != marker.Seq && claim.Writer != device {
		fmt.Fprintf(os.Stderr, "Continuing from %s's checkpoint.\n", claim.Writer)
		if err = catchUpLocalRunner(o, id, &q); err != nil {
			release()
			return err
		}
	} else if err = syncWorkspace(o.remote, o.binding, o.root, "push", false, o.includeIgnored); err != nil {
		release()
		return fmt.Errorf("copy local changes to the remote before starting: %w", err)
	}
	err = updateLocalRunnerMarker(id, func(m *localRunnerMarker) {
		m.Remote, m.Workspace, m.Root, m.Seq, m.Epoch, m.Lost = o.remoteName, o.binding.Workspace, o.root, claim.Seq, claim.Epoch, false
	})
	if err != nil {
		release()
		return err
	}
	if q.Conversation == "" && !q.New && !q.Resume && !q.ResumeLatest {
		q.Conversation = claim.Conversation
		if paths, pathErr := chatDaemonPathSet(id); pathErr == nil {
			if previous, ok, _ := readSavedChatDaemonState(id, paths); ok && previous.Conversation != "" {
				q.Conversation = previous.Conversation
			}
		}
	}
	fmt.Fprintf(os.Stderr, "The agent runs on this device (%s); %s keeps a copy after every turn.\n", device, o.remoteName)
	conn, err := startLocalRunnerDaemon(q)
	if err != nil {
		release()
		return err
	}
	return finishLocalRunnerUI(id, runRemoteChatClientConnection(q, conn))
}

// catchUpLocalRunner brings the checkout and agent state to the newest
// checkpoint, which another device or the remote host wrote.
func catchUpLocalRunner(o localRunnerOpen, id string, q *Request) error {
	if err := setAsideLocalWork(o, id); err != nil {
		return err
	}
	if err := syncWorkspace(o.remote, o.binding, o.root, "pull", true, o.includeIgnored, true); err != nil {
		return fmt.Errorf("bring this checkout up to date: %w", err)
	}
	var bundle runnerBundle
	if err := rpc(o.remote, Request{Op: "runner-export", ID: o.binding.Workspace}, nil, &bundle); err != nil {
		return err
	}
	native, err := importConversation(bundle, o.root)
	if err != nil {
		return err
	}
	if !native {
		fmt.Fprintln(os.Stderr, "The agent's own session could not be restored here; the conversation continues from Jara's transcript.")
	}
	if q.Conversation == "" && !q.New {
		q.Conversation, q.Resume, q.ResumeLatest = bundle.ThreadID, false, false
	}
	return nil
}

func finishLocalRunnerUI(id string, err error) error {
	if loadLocalRunnerMarker(id).Lost {
		fmt.Fprintln(os.Stderr, "Another device continued this conversation, so the agent here stopped. "+
			"Run jara --local again to continue it on this device; unsynced local work is set aside first.")
	}
	return err
}
