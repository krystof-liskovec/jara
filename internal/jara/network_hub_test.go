package jara

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	done := make(chan string)
	go func() { data, _ := io.ReadAll(read); done <- string(data) }()
	runErr := fn()
	os.Stdout = original
	write.Close()
	return <-done, runErr
}

func networkTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JARA_DATA", filepath.Join(home, ".local", "share", "jara"))
	t.Setenv("JARA_PEER_KEYS_COMMAND", "")
}

func authorizedLines(t *testing.T) string {
	t.Helper()
	out, err := captureStdout(t, func() error { return runPeerKeys(os.Getenv("HOME")) })
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestJoinCodeIsSingleUseAndAuthorizesTheDevice(t *testing.T) {
	networkTestHome(t)
	key := authorizedKey(joinKey([]byte("0123456789abcdef")).Public().(ed25519.PublicKey))
	registry := []networkHost{{Name: "work", Host: "work.tailnet", Port: 2222, HostKeys: []string{"ssh-ed25519 AAAAhost"}}}
	body, _ := json.Marshal(networkInvite{Key: key, Expires: time.Now().Add(10 * time.Minute).Unix(), Registry: registry})
	out, err := captureStdout(t, func() error { return networkRPC(Request{Op: "network-invite"}, bytes.NewReader(body)) })
	if err != nil {
		t.Fatal(err)
	}
	var answer struct{ ID string }
	if err = json.Unmarshal([]byte(out), &answer); err != nil || answer.ID != inviteID(key) {
		t.Fatalf("invite answer %q: %v", out, err)
	}
	lines := authorizedLines(t)
	if !strings.Contains(lines, `restrict,command="jara _join `+answer.ID+`" `+key) {
		t.Fatalf("sshd does not restrict the invitation's key:\n%s", lines)
	}
	// The managed authorized_keys block, for hosts without the sshd hook.
	managed, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".ssh", "authorized_keys"))
	if !strings.Contains(string(managed), "jara _join "+answer.ID) {
		t.Fatalf("managed block lacks the invitation:\n%s", managed)
	}

	device := "ssh-ed25519 AAAAdevice"
	request := `{"device":"laptop","key":"` + device + `"}`
	out, err = captureStdout(t, func() error { return runJoin(answer.ID, strings.NewReader(request)) })
	if err != nil {
		t.Fatal(err)
	}
	var joined joinAnswer
	if err = json.Unmarshal([]byte(out), &joined); err != nil || joined.Device != "laptop" || len(joined.Registry) != 1 || joined.Registry[0].Port != 2222 {
		t.Fatalf("join answer %q: %v", out, err)
	}
	lines = authorizedLines(t)
	if strings.Contains(lines, "_join") || !strings.Contains(lines, device+" jara-device:laptop") {
		t.Fatalf("after joining, sshd accepts:\n%s", lines)
	}
	if _, err = captureStdout(t, func() error { return runJoin(answer.ID, strings.NewReader(request)) }); err == nil {
		t.Fatal("a join code worked twice")
	}
	out, _ = captureStdout(t, func() error { return networkRPC(Request{Op: "network-joined", ID: answer.ID}, nil) })
	if !strings.Contains(out, `"device":"laptop"`) {
		t.Fatalf("the inviting device does not learn of the join: %q", out)
	}

	// Another device of the same name keeps its key; the newcomer is renamed.
	if got := uniqueDeviceName("laptop", "ssh-ed25519 AAAAother"); got != "laptop-2" {
		t.Fatalf("name for a second laptop = %q", got)
	}
	if _, err = captureStdout(t, func() error { return networkRPC(Request{Op: "network-revoke", Runner: "laptop"}, nil) }); err != nil {
		t.Fatal(err)
	}
	if lines = authorizedLines(t); strings.Contains(lines, device) {
		t.Fatalf("a revoked device can still log in:\n%s", lines)
	}
}

func TestExpiredJoinCodesAreRefused(t *testing.T) {
	networkTestHome(t)
	key := authorizedKey(joinKey([]byte("fedcba9876543210")).Public().(ed25519.PublicKey))
	invite := networkInvite{Key: key, Expires: time.Now().Add(-time.Minute).Unix(), Registry: []networkHost{{Name: "work"}}}
	if err := os.MkdirAll(filepath.Join(networkDir(), "invites"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(invite)
	if err := os.WriteFile(filepath.Join(networkDir(), "invites", inviteID(key)+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if lines := authorizedLines(t); strings.Contains(lines, key) {
		t.Fatalf("sshd accepts an expired invitation:\n%s", lines)
	}
	if _, err := captureStdout(t, func() error { return runJoin(inviteID(key), strings.NewReader(`{}`)) }); err == nil {
		t.Fatal("an expired code worked")
	}
	long, _ := json.Marshal(networkInvite{Key: key, Expires: time.Now().Add(3 * time.Hour).Unix(), Registry: invite.Registry})
	if _, err := captureStdout(t, func() error { return networkRPC(Request{Op: "network-invite"}, bytes.NewReader(long)) }); err == nil {
		t.Fatal("accepted an invitation that lasts hours")
	}
}
