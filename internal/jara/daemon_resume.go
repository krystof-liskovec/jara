package jara

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// daemonResume is a session of a local-only folder that stopped while its
// agent was working (the device shut down, or the session was killed). The
// Jara daemon continues it when it next starts; otherwise the next window on
// the folder does.
type daemonResume struct {
	Executable string   `json:"executable"`
	Request    Request  `json:"request"`
	Env        []string `json:"env,omitempty"`
	At         int64    `json:"at"`
}

// daemonResumeMaxAge drops interrupted work too old to continue sensibly.
const daemonResumeMaxAge = 7 * 24 * time.Hour

const localContinueNote = "[Jara: this device shut down, or this session was stopped, while you were working on the latest request. " +
	"The working tree holds the changes made so far. Check where the work stands, then continue the unfinished task.]"

func daemonDir() string { return filepath.Join(fsutil.DataRoot(), "daemon") }

func daemonSocketPath() string { return filepath.Join(daemonDir(), "daemon.sock") }

func daemonResumePath(id string) string {
	return filepath.Join(daemonDir(), "resume", id+".json")
}

// saveDaemonResume records q's session to continue later. A running turn
// continues with a note; scheduled work alone just runs when it restarts.
func saveDaemonResume(q Request, state chatDaemonState) error {
	if state.Conversation == "" || !nameRE.MatchString(q.ID) {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	q.Conversation, q.New, q.Resume, q.ResumeLatest = state.Conversation, false, false, false
	q.Prompt, q.BackgroundPromptID, q.Headless, q.Continue = "", "", true, ""
	if state.ActiveTurn {
		q.Continue = localContinueNote
	}
	entry := daemonResume{Executable: executable, Request: q, Env: os.Environ(), At: time.Now().Unix()}
	if err = os.MkdirAll(filepath.Dir(daemonResumePath(q.ID)), 0700); err != nil {
		return err
	}
	return fsutil.WriteJSON(daemonResumePath(q.ID), entry)
}

// takeDaemonResume removes and returns the interrupted session of id.
func takeDaemonResume(id string) (daemonResume, bool) {
	path := daemonResumePath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return daemonResume{}, false
	}
	_ = os.Remove(path)
	var entry daemonResume
	if json.Unmarshal(data, &entry) != nil || entry.Request.ID != id ||
		time.Since(time.Unix(entry.At, 0)) > daemonResumeMaxAge {
		return daemonResume{}, false
	}
	return entry, true
}

// pendingDaemonResumes lists the sessions waiting to be continued.
func pendingDaemonResumes() []string {
	entries, _ := os.ReadDir(filepath.Join(daemonDir(), "resume"))
	var ids []string
	for _, entry := range entries {
		if id, ok := strings.CutSuffix(entry.Name(), ".json"); ok && nameRE.MatchString(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// applyDaemonResume lets a window opening a folder continue the work its
// session was interrupted in, when the daemon has not already done so.
func applyDaemonResume(q *Request) {
	if q.New || q.Continue != "" {
		return
	}
	entry, ok := takeDaemonResume(q.ID)
	if !ok || q.Conversation != "" && q.Conversation != entry.Request.Conversation {
		return
	}
	if q.Resume || q.ResumeLatest {
		return // the user picks a conversation; the interrupted one waits in history
	}
	q.Conversation, q.Continue = entry.Request.Conversation, entry.Request.Continue
}

// daemonMessage is one request to the Jara daemon, and its answer.
type daemonMessage struct {
	Op         string   `json:"op"` // ping, spawn
	Executable string   `json:"executable,omitempty"`
	Command    string   `json:"command,omitempty"`
	Request    *Request `json:"request,omitempty"`
	Log        string   `json:"log,omitempty"`
	Env        []string `json:"env,omitempty"`

	PID      int    `json:"pid,omitempty"`
	Version  string `json:"version,omitempty"`
	Sessions int    `json:"sessions,omitempty"`
	Error    string `json:"error,omitempty"`
}

var errNoDaemon = errors.New("the Jara daemon is not running")

// callDaemon sends one request to the Jara daemon of this user.
func callDaemon(request daemonMessage) (daemonMessage, error) {
	conn, err := net.DialTimeout("unix", daemonSocketPath(), 500*time.Millisecond)
	if err != nil {
		return daemonMessage{}, errNoDaemon
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err = json.NewEncoder(conn).Encode(request); err != nil {
		return daemonMessage{}, err
	}
	var answer daemonMessage
	if err = json.NewDecoder(conn).Decode(&answer); err != nil {
		return daemonMessage{}, fmt.Errorf("read the Jara daemon's answer: %w", err)
	}
	if answer.Error != "" {
		return answer, errors.New(answer.Error)
	}
	return answer, nil
}

// spawnViaDaemon starts a session process as a child of the Jara daemon, so
// it lives in the daemon's service rather than this terminal's, survives
// logging out, and is stopped gracefully at shutdown. It runs this binary
// with this process's environment, exactly as starting it directly would.
func spawnViaDaemon(command string, request Request, logPath string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	saveDeviceEnvironment()
	answer, err := callDaemon(daemonMessage{Op: "spawn", Executable: executable, Command: command,
		Request: &request, Log: logPath, Env: os.Environ()})
	if err != nil {
		return 0, err
	}
	return answer.PID, nil
}
