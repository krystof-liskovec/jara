package jara

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	testHostKeyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHostKeyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestNetworkHostsShareAndExcludeThisHost(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	body := `[{"name":"personal","host":"personal.ts.net","port":2222,"user":"agent","host_keys":["` + testHostKeyA + `"]},` +
		`{"name":"work","host":"work.ts.net","user":"agent","host_keys":["` + testHostKeyB + `"]}]`
	if err := saveNetworkHosts(strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(networkHostsPath()); err != nil {
		t.Fatal(err)
	}
	var hosts []networkHost
	data, _ := os.ReadFile(networkHostsPath())
	if err := json.Unmarshal(data, &hosts); err != nil || len(hosts) != 2 {
		t.Fatalf("stored hosts: %v %+v", err, hosts)
	}
	others := excludeHost(hosts, []string{testHostKeyA})
	if len(others) != 1 || others[0].Name != "work" {
		t.Fatalf("this host was not left out: %+v", others)
	}
}

func TestNetworkHostsRejectUnsafeEntries(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	for _, body := range []string{
		`[{"name":"x","host":"-oProxyCommand=evil","host_keys":["` + testHostKeyA + `"]}]`,
		`[{"name":"x","host":"ok.example","user":"-oProxyCommand=evil","host_keys":["` + testHostKeyA + `"]}]`,
		`[{"name":"x","host":"ok.example","host_keys":[]}]`,
		`[{"name":"x","host":"ok.example","host_keys":["ssh-ed25519 AAAA\nevil.example ssh-ed25519 AAAA"]}]`,
		`[{"name":"../x","host":"ok.example","host_keys":["` + testHostKeyA + `"]}]`,
	} {
		if err := saveNetworkHosts(strings.NewReader(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestHopPinsHostKeysAndUsesOnlyTheForwardedAgent(t *testing.T) {
	h := networkHost{Name: "work", Host: "work.ts.net", Port: 2222, User: "agent", HostKeys: []string{testHostKeyA, testHostKeyB}}
	known := hopKnownHosts(h)
	if known != "[work.ts.net]:2222 "+testHostKeyA+"\n[work.ts.net]:2222 "+testHostKeyB+"\n" {
		t.Fatalf("known hosts: %q", known)
	}
	if got := hopKnownHosts(networkHost{Host: "work.ts.net", Port: 22, HostKeys: []string{testHostKeyA}}); got != "work.ts.net "+testHostKeyA+"\n" {
		t.Fatalf("default port known hosts: %q", got)
	}
	args := hopSSHArgs(h, "/data/known")
	for _, want := range []string{"StrictHostKeyChecking=yes", "UserKnownHostsFile=/data/known", "GlobalKnownHostsFile=/dev/null", "PasswordAuthentication=no", "ForwardAgent=no"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %s: %v", want, args)
		}
	}
	if tail := args[len(args)-3:]; !slices.Equal(tail, []string{"--", "work.ts.net", "jara connect"}) {
		t.Fatalf("command: %v", tail)
	}
	if networkHostAddress(h) != "agent@work.ts.net:2222" {
		t.Fatalf("address: %s", networkHostAddress(h))
	}
}

func TestNewConversationRowsComeFirstOnlyForReadyProjects(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no claude: Codex only
	ready := conversationProject{Workspace: Workspace{ID: "w", Ready: true}, Threads: []Conversation{{ID: "c"}}}
	rows := newConversationRows(ready)
	if len(rows) != 1 || rows[0].Kind != "new" || rows[0].Workspace != "w" || rows[0].Agent != "codex" {
		t.Fatalf("rows: %+v", rows)
	}
	if rows := newConversationRows(conversationProject{Workspace: Workspace{ID: "w"}}); len(rows) != 0 {
		t.Fatalf("a project without files offered: %+v", rows)
	}
}

func TestDeviceDetailCountsSessions(t *testing.T) {
	device := deviceInfo{Name: "desktop", Folders: []deviceFolder{{Folder: "/a", Running: true, Busy: true}, {Folder: "/b", Running: true}, {Folder: "/c"}}}
	if got := deviceDetail(device); got != "3 folders · 2 running · 1 working" {
		t.Fatalf("detail: %s", got)
	}
	if got := deviceDetail(deviceInfo{Error: "offline"}); got != "Unavailable: offline" {
		t.Fatalf("detail: %s", got)
	}
}
