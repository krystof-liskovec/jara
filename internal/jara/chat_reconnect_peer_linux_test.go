//go:build linux

package jara

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReconnectingRelayArgs(t *testing.T) {
	reconnecting := token(Request{UserInterface: "jara", ID: "w-1", Reconnect: true})
	legacy := token(Request{UserInterface: "jara", ID: "w-1"})
	for name, test := range map[string]struct {
		args []string
		want bool
	}{
		"reconnecting relay": {[]string{"/usr/local/bin/jara", "_chat-server", reconnecting}, true},
		"older client relay": {[]string{"jara", "_chat-server", legacy}, false},
		"direct SSH client":  {[]string{"jara", "connect", reconnecting}, false},
		"shell wrapper":      {[]string{"sh", "-c", "jara _chat-server " + reconnecting}, false},
		"other program":      {[]string{"notjara", "_chat-server", reconnecting}, false},
		"invalid token":      {[]string{"jara", "_chat-server", "%%%"}, false},
		"missing token":      {[]string{"jara", "_chat-server"}, false},
		"empty command line": {nil, false},
	} {
		if got := reconnectingRelayArgs(test.args); got != test.want {
			t.Errorf("%s: got %v, want %v", name, got, test.want)
		}
	}
}

func TestLinkArgs(t *testing.T) {
	link := token(Request{Runner: "phone", Link: "0123456789abcdef"})
	for name, test := range map[string]struct {
		args []string
		want bool
	}{
		"device link":   {[]string{"/usr/local/bin/jara", "_device-link", link}, true},
		"runner link":   {[]string{"jara", "_runner-link", link}, true},
		"missing nonce": {[]string{"jara", "_device-link", token(Request{Runner: "phone"})}, false},
		"device open":   {[]string{"jara", "_device-open", link}, false},
		"shell wrapper": {[]string{"sh", "-c", "jara _device-link " + link}, false},
		"invalid token": {[]string{"jara", "_device-link", "%%%"}, false},
		"missing token": {[]string{"jara", "_device-link"}, false},
		"empty":         {nil, false},
	} {
		if got := linkArgs(test.args); got != test.want {
			t.Errorf("%s: got %v, want %v", name, got, test.want)
		}
	}
}

func TestChatPeerPIDIdentifiesSocketPeer(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "peer.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if pid := chatPeerPID(server); pid != os.Getpid() {
		t.Fatalf("peer PID = %d, want %d", pid, os.Getpid())
	}
	if reconnectingChatRelay(os.Getpid()) {
		t.Fatal("the test process was taken for a relay")
	}
}

func TestProcessAncestorsWalksToTheRelay(t *testing.T) {
	child := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	ancestors := processAncestors(func(pid int) bool { return pid == child.Process.Pid })
	if !ancestors[os.Getpid()] {
		t.Fatalf("ancestors %v omit the test process %d", ancestors, os.Getpid())
	}
	if ancestors[child.Process.Pid] {
		t.Fatal("the relay itself was listed as its own ancestor")
	}
}
