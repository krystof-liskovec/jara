//go:build linux || darwin

package jara

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func askSupervisor(t *testing.T, s *daemonSupervisor, request daemonMessage) daemonMessage {
	t.Helper()
	client, server := net.Pipe()
	go s.serve(server)
	defer client.Close()
	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatal(err)
	}
	var answer daemonMessage
	if err := json.NewDecoder(client).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func TestDaemonSupervisorSpawnsSessionsAndStopsThemGracefully(t *testing.T) {
	dir := shortTempDir(t)
	stopped := filepath.Join(dir, "stopped")
	script := filepath.Join(dir, "fake-jara")
	// The fake session reports its command, environment and SIGTERM, which
	// is how the daemon asks a session to hand its work over.
	body := "#!/bin/sh\necho \"$1 $JARA_TEST_MARK\" > " + filepath.Join(dir, "started") +
		"\ntrap 'touch " + stopped + "; exit 0' TERM\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	s := &daemonSupervisor{children: map[int]*exec.Cmd{}, exited: make(chan struct{}, 1)}
	request := Request{ID: "local-abc123", Root: dir}
	log := filepath.Join(dir, "session.log")

	refused := askSupervisor(t, s, daemonMessage{Op: "spawn", Executable: script, Command: "_rpc", Request: &request, Log: log})
	if !strings.Contains(refused.Error, "does not start") {
		t.Fatalf("spawning an arbitrary command = %+v", refused)
	}
	answer := askSupervisor(t, s, daemonMessage{Op: "spawn", Executable: script, Command: "_runner-daemon",
		Request: &request, Log: log, Env: []string{"JARA_TEST_MARK=client-env", "PATH=" + os.Getenv("PATH")}})
	if answer.Error != "" || answer.PID == 0 {
		t.Fatalf("spawn = %+v", answer)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(dir, "started"))
		if strings.TrimSpace(string(data)) == "_runner-daemon client-env" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session did not start with the client's environment: %q", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ping := askSupervisor(t, s, daemonMessage{Op: "ping"}); ping.Sessions != 1 || ping.Version != Version {
		t.Fatalf("ping = %+v", ping)
	}
	s.stopAll(5 * time.Second)
	if _, err := os.Stat(stopped); err != nil {
		t.Fatal("the session was not asked to stop gracefully")
	}
	if ping := askSupervisor(t, s, daemonMessage{Op: "ping"}); ping.Sessions != 0 {
		t.Fatalf("sessions after stopping = %d", ping.Sessions)
	}
}
