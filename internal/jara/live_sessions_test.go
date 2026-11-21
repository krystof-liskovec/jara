package jara

import (
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

func TestAttachNeverReplacesAnOfflineDevice(t *testing.T) {
	runnerTestHomes(t)
	makeRunnerWorkspace(t, "ws1")
	paths, err := runnerPathSet("ws1")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := claimRunner(Request{ID: "ws1", Runner: "laptop"}, paths)
	if err != nil {
		t.Fatal(err)
	}
	if sessions := hubLiveSessions(); len(sessions) != 1 || sessions[0].Runner != "laptop" || sessions[0].Workspace != "ws1" {
		t.Fatalf("live sessions with an online device = %+v", sessions)
	}
	if _, err = updateRunnerLease(paths, "ws1", func(lease *runnerLease) error {
		lease.LastSeen = time.Now().Add(-time.Hour).Unix()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sessions := hubLiveSessions(); len(sessions) != 0 {
		t.Fatalf("an offline device's session is listed as running: %+v", sessions)
	}
	q := Request{ID: "ws1", Attach: true}
	if conn, err := hubRunnerRoute(&q); conn != nil || err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("attaching to an offline device's session = %v %v", conn, err)
	}
	if lease, _, _ := loadRunnerLease(paths, "ws1"); !lease.holds("laptop", claim.Epoch) {
		t.Fatalf("attaching replaced the device: %+v", lease)
	}
	// Without a device holding it, attaching goes to the host's own session.
	if _, err = updateRunnerLease(paths, "ws1", func(lease *runnerLease) error {
		lease.Owner = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if conn, err := hubRunnerRoute(&q); conn != nil || err != nil {
		t.Fatalf("attaching to the host's session = %v %v", conn, err)
	}
	if err = runRemoteChatServer(Request{UserInterface: "jara", ID: "ws1", Attach: true}); err == nil ||
		!strings.Contains(err.Error(), "no Jara session is running") {
		t.Fatalf("attaching started a session: %v", err)
	}
}

func TestHubWorkspaceNameUsesTheOwnersName(t *testing.T) {
	runnerTestHomes(t)
	path := makeRunnerWorkspace(t, "w-0123")
	if name := hubWorkspaceName("w-0123"); name != "w-0123" {
		t.Fatalf("unnamed workspace = %q", name)
	}
	if err := fsutil.WriteJSON(path+".json", Workspace{ID: "w-0123", Path: path, URL: "https://git.example/me/api.git"}); err != nil {
		t.Fatal(err)
	}
	if name := hubWorkspaceName("w-0123"); name != "api" {
		t.Fatalf("cloned workspace = %q", name)
	}
}

func TestChooseLiveSession(t *testing.T) {
	sessions := []liveSession{
		{Local: "local-aa", Name: "jara", Folder: "/src/jara", Runner: "laptop"},
		{Workspace: "w-1", Name: "api", Remote: "work", Runner: runnerHub},
		{Workspace: "w-2", Name: "api", Remote: "home", Runner: "desktop", Folder: "/src/api"},
	}
	for target, want := range map[string]string{"1": "local-aa", "3": "w-2", "w-1": "w-1", "local-aa": "local-aa", "jara": "local-aa", "/src/api": "w-2"} {
		got, err := chooseLiveSession(sessions, nil, target, strings.NewReader(""))
		if err != nil || got.target() != want {
			t.Errorf("attach %q = %q %v, want %q", target, got.target(), err, want)
		}
	}
	for _, target := range []string{"api", "4", "0", "nothing"} {
		if got, err := chooseLiveSession(sessions, nil, target, strings.NewReader("")); err == nil {
			t.Errorf("attach %q = %+v, want an error", target, got)
		}
	}
	got, err := chooseLiveSession(sessions, nil, "", strings.NewReader("2\n"))
	if err != nil || got.target() != "w-1" {
		t.Fatalf("picked = %+v %v", got, err)
	}
	if _, err = chooseLiveSession(nil, nil, "", strings.NewReader("")); err == nil {
		t.Fatal("attached with nothing running")
	}
}

func TestLiveSessionWhere(t *testing.T) {
	for want, s := range map[string]liveSession{
		"this device":        {Local: "local-aa", Remote: "work", Runner: "laptop"},
		"work":               {Remote: "work", Runner: runnerHub},
		"desktop (via work)": {Remote: "work", Runner: "desktop"},
	} {
		if got := s.where(); got != want {
			t.Errorf("where = %q, want %q", got, want)
		}
	}
}
