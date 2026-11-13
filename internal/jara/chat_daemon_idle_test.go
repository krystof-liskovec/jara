package jara

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

func writeIdleDaemonLock(t *testing.T, id string, state chatDaemonState) (string, chatDaemonPaths) {
	t.Helper()
	paths, err := chatDaemonPathSet(id)
	if err != nil {
		t.Fatal(err)
	}
	pid := fmt.Sprintf("%d\n", os.Getpid())
	if err = os.WriteFile(paths.pid, []byte(pid), 0600); err != nil {
		t.Fatal(err)
	}
	state.WorkspaceID, state.PID = id, os.Getpid()
	if err = fsutil.WriteJSON(paths.state, state); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(fsutil.DataRoot(), "workspaces", id+".lock")
	if err = os.MkdirAll(lock, 0700); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"chat-daemon.pid", workspaceLockOwnerFile} {
		if err = os.WriteFile(filepath.Join(lock, marker), []byte(pid), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return lock, paths
}

func TestIdleDetachedChatDaemonDoesNotBlockUpdates(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	lock, _ := writeIdleDaemonLock(t, "w-idle", chatDaemonState{Conversation: "th-1"})
	if id, idle := idleChatDaemonWorkspace(lock, false); !idle || id != "w-idle" {
		t.Fatalf("idle detached session = %q %v, want stoppable", id, idle)
	}
}

func TestBusyChatDaemonBlocksUpdates(t *testing.T) {
	cases := map[string]struct {
		state     chatDaemonState
		schedules string
		crunch    string
	}{
		"active turn":      {state: chatDaemonState{ActiveTurn: true}},
		"attached client":  {state: chatDaemonState{AttachedClients: 1}},
		"legacy attached":  {state: chatDaemonState{Attached: true}},
		"pending schedule": {schedules: `[{"status":"pending"}]`},
		"running crunch":   {crunch: `{"status":"waitingUsage"}`},
		"corrupt schedule": {schedules: `{`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("JARA_DATA", t.TempDir())
			lock, paths := writeIdleDaemonLock(t, "w-busy", c.state)
			if c.schedules != "" {
				if err := os.WriteFile(paths.schedules, []byte(c.schedules), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if c.crunch != "" {
				if err := os.WriteFile(paths.crunch, []byte(c.crunch), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, idle := idleChatDaemonWorkspace(lock, false); idle {
				t.Fatal("busy session treated as idle")
			}
		})
	}
}

func TestWorkspaceOperationLockBlocksUpdates(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	lock, _ := writeIdleDaemonLock(t, "w-op", chatDaemonState{})
	if err := os.Remove(filepath.Join(lock, "chat-daemon.pid")); err != nil {
		t.Fatal(err)
	}
	if _, idle := idleChatDaemonWorkspace(lock, false); idle {
		t.Fatal("non-daemon workspace operation treated as an idle session")
	}
	if err := os.WriteFile(filepath.Join(lock, "chat-daemon.pid"), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, idle := idleChatDaemonWorkspace(lock, false); idle {
		t.Fatal("lock owned by another process treated as an idle session")
	}
}

// startIdleDaemon runs a real daemon loop on the data-root paths and removes
// its lock and PID file on exit, as runChatDaemon's deferred cleanup does.
func startIdleDaemon(t *testing.T) (*daemonHarness, string) {
	t.Helper()
	root := shortTempDir(t)
	t.Setenv("JARA_DATA", root)
	state := chatDaemonState{WorkspaceID: "w-test", Conversation: "th-1", ThreadLoaded: true, PID: os.Getpid()}
	lock, paths := writeIdleDaemonLock(t, "w-test", state)
	h := startDaemonHarness(t, daemonHarnessOptions{state: state, dir: filepath.Dir(paths.socket)})
	go func() {
		err := <-h.done
		_ = os.RemoveAll(lock)
		_ = os.Remove(paths.pid)
		h.done <- err
	}()
	return h, lock
}

func TestStopIdleChatDaemonStopsDetachedSession(t *testing.T) {
	h, lock := startIdleDaemon(t)
	if err := releaseIdleChatDaemon("w-test"); err != nil {
		t.Fatal(err)
	}
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatalf("workspace lock remains after stopping the idle session: %v", err)
	}
}

func TestStopIdleChatDaemonLeavesAttachedSession(t *testing.T) {
	h, _ := startIdleDaemon(t)
	client, _ := h.attach()
	h.waitState("attached", func(s chatDaemonState) bool { return s.AttachedClients == 1 })
	err := releaseIdleChatDaemon("w-test")
	if err == nil || !strings.Contains(err.Error(), "client is attached") {
		t.Fatalf("release with attached client = %v", err)
	}
	h.assertRunning()
	client.send(map[string]string{"method": "jara/close"})
	if err = h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestStopIdleChatDaemonWithoutSession(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	if err := releaseIdleChatDaemon("w-none"); err != nil {
		t.Fatal(err)
	}
}
