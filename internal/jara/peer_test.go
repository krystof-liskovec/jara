package jara

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeerKeyMayOnlyRunStandbyOperations(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	for command, allowed := range map[string]bool{
		"'jara' '_rpc' '" + token(Request{Op: "standby-prepare", ID: "w-1"}) + "'":  true,
		"'jara' '_rpc' '" + token(Request{Op: "runner-claim", ID: "w-1"}) + "'":     false,
		"'jara' '_rpc' '" + token(Request{Op: "workspace-delete", ID: "w-1"}) + "'": false,
		"sh -c 'cat ~/.ssh/id_ed25519'":                                             false,
		"'jara' '_open' '" + token(Request{ID: "w-1"}) + "'":                        false,
	} {
		t.Setenv("SSH_ORIGINAL_COMMAND", command)
		err := runPeerCommand("primary", strings.NewReader(""))
		refused := err != nil && (strings.Contains(err.Error(), "may not run") || strings.Contains(err.Error(), "may only run"))
		if refused == allowed {
			t.Errorf("%.40s…: allowed %v, err %v", command, allowed, err)
		}
	}
}

func TestPeerKeysAreAlwaysForcedAndManagedBlockIsReplaced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JARA_DATA", filepath.Join(home, ".local", "share", "jara"))
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGmYd/fPmJnjkU/bns2eSwvNi0nfvOg5+5P1FXYAg2XF"
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAAmine me@laptop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := authorizePeers(map[string]string{"jelly": key}); err != nil {
		t.Fatal(err)
	}
	// A key planted in the peer directory gets the forced command too.
	if err := os.WriteFile(filepath.Join(peerDir(), "authorized", "evil.pub"), []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, line := range peerAuthorizedLines(peerDir()) {
		if !strings.HasPrefix(line, `restrict,command="jara _peer `) {
			t.Fatalf("unrestricted peer key: %s", line)
		}
	}
	if err := authorizePeers(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if string(data) != "ssh-ed25519 AAAAmine me@laptop\n" {
		t.Fatalf("authorized_keys after removing peers:\n%s", data)
	}
	if authorizePeers(map[string]string{"x": "ssh-ed25519 AAAA\ncommand=\"sh\" ssh-ed25519 AAAA"}) == nil {
		t.Fatal("a key with a line break was accepted")
	}
}

func TestStandbyChainIsPrimaryThenMarkedRemotes(t *testing.T) {
	c := Config{Default: "jelly", Order: []string{"jelly", "work", "backup", "spare"},
		Remotes: map[string]Remote{"jelly": {Host: "j"}, "work": {Host: "w"}, "backup": {Host: "b", Standby: true}, "spare": {Host: "s", Standby: true}}}
	if got := strings.Join(c.standbyChain(), " "); got != "jelly backup spare" {
		t.Fatalf("chain = %s", got)
	}
	if err := c.rankRemotes([]string{"spare"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.standbyChain(), " "); got != "spare backup" {
		t.Fatalf("chain after promoting a standby = %s", got)
	}
	for _, address := range []string{"backup.example.org:2222", "10.0.0.5", "[fd00::1]:22"} {
		if _, _, err := splitPeerAddress(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	if _, _, err := splitPeerAddress("host:99999"); err == nil {
		t.Error("invalid port accepted")
	}
}

func TestPrimaryForwardsStoredSessionFilesAsDeltas(t *testing.T) {
	base := t.TempDir()
	primary, standby := filepath.Join(base, "primary"), filepath.Join(base, "standby")
	t.Setenv("JARA_DATA", primary)
	paths, err := runnerPathSet("w-forward")
	if err != nil {
		t.Fatal(err)
	}
	backend := "11111111-2222-3333-4444-555555555555"
	line := `{"type":"message","text":"forwarded"}` + "\n"
	deviceCheckpoint := func(text string, base int64) {
		file := runnerNativeFile{Agent: "claude", BackendID: backend, Base: base}
		file.setData([]byte(text))
		if base > 0 {
			file.Tail, _ = fileTail(paths.storedNativePath("th-1", file), base)
		}
		bundle := runnerBundle{ThreadID: "th-1", Thread: []byte(`{"id":"th-1"}`), Files: []runnerNativeFile{file}}
		if _, err := updateRunnerLease(paths, "w-forward", func(lease *runnerLease) error {
			return storeRunnerCheckpoint(paths, lease, runnerCheckpoint{Current: "th-1", Threads: []runnerBundle{bundle}}, "laptop")
		}); err != nil {
			t.Fatal(err)
		}
	}
	forward := func(status *standbyStatus) int {
		t.Setenv("JARA_DATA", primary)
		lease, _, _ := loadRunnerLease(paths, "w-forward")
		sent := 0
		_, stored, err := sendCheckpoint(status.Sent, true, "standby",
			func(state *runnerSentState) (runnerCheckpoint, error) {
				t.Setenv("JARA_DATA", primary)
				checkpoint, err := exportStoredCheckpoint(paths, lease, status.Seq, state)
				for _, bundle := range checkpoint.Threads {
					for _, file := range bundle.Files {
						sent += file.size()
					}
				}
				return checkpoint, err
			},
			func(checkpoint runnerCheckpoint) (runnerCheckpointAnswer, error) {
				t.Setenv("JARA_DATA", standby)
				return storeStandbyCheckpoint(Request{ID: "w-forward", Peer: "primary"}, standbyCheckpoint{Checkpoint: checkpoint, Lease: lease})
			})
		if err != nil {
			t.Fatal(err)
		}
		status.Seq, status.Sent = lease.Seq, stored
		return sent
	}
	deviceCheckpoint(strings.Repeat(line, 100), 0)
	var status standbyStatus
	if sent := forward(&status); sent != 100*len(line) {
		t.Fatalf("first forward sent %d bytes", sent)
	}
	t.Setenv("JARA_DATA", primary)
	deviceCheckpoint(line, int64(100*len(line)))
	if sent := forward(&status); sent != len(line) {
		t.Fatalf("second forward sent %d bytes, want only the appended line", sent)
	}
	t.Setenv("JARA_DATA", standby)
	standbyPaths, _ := runnerPathSet("w-forward")
	data, _ := os.ReadFile(standbyPaths.storedNativePath("th-1", runnerNativeFile{Agent: "claude", BackendID: backend}))
	if string(data) != strings.Repeat(line, 101) {
		t.Fatalf("standby copy has %d bytes", len(data))
	}
}
