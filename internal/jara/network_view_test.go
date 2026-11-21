package jara

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rivo/uniseg"
	"krystof.liskovec/jara/internal/fsutil"
)

func sampleNetworkView(now time.Time) networkView {
	return networkView{Here: "laptop",
		Hosts: []networkHostView{
			{Name: "work", Address: "work.tailnet.ts.net:2222", Version: "0.1.0-dev", Role: "primary", Online: true},
			{Name: "old-vps", Address: "203.0.113.9", Online: true, Outdated: true, Problem: "needs jara deployment upgrade --remote old-vps"},
			{Name: "lab", Address: "lab.tailnet.ts.net", Problem: "remote network-status: exit status 255: ssh: connect to host lab.tailnet.ts.net port 22: Connection timed out"},
		},
		Devices: []networkDeviceView{
			{Name: "laptop", This: true, Callable: true, Daemon: true, Via: []string{"work"}, Folders: 5, Running: 1},
			{Name: "desktop", Callable: true, Via: []string{"work"}, Folders: 1},
			{Name: "mini", Callable: true, Via: []string{"work"}, Problem: "device mini: the Codex CLI is not installed"},
			{Name: "phone-vm", Joined: true, LastSeen: now.Add(-2 * time.Hour).Unix()},
		},
		Sessions: []liveSession{
			{Name: "api", Runner: "desktop", Remote: "work", DeviceFolder: "/src/api", Busy: true, Attached: 2},
		}}
}

func TestRenderNetworkAlignsEverySection(t *testing.T) {
	now := time.Date(2026, 11, 21, 14, 5, 0, 0, time.Local)
	out := renderNetwork(sampleNetworkView(now), now, networkPainter{})
	for _, want := range []string{
		"seen from laptop at 14:05", "work.tailnet.ts.net:2222  primary  0.1.0-dev", "needs jara deployment upgrade",
		"unreachable · Connection timed out", "this device", "5 folders · 1 running", "1 folder", "joined",
		"last linked 2 h ago", "the Codex CLI is not installed", "desktop (via work)  working  2 windows",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "device mini:") || strings.Contains(out, "\x1b[") {
		t.Fatalf("repeated device name or colour without a terminal:\n%s", out)
	}
	// Each device's call status starts in the same column.
	column := -1
	for _, line := range strings.Split(out, "\n") {
		for _, status := range []string{"accepts calls", "offline"} {
			if i := strings.Index(line, status); i >= 0 {
				at := uniseg.StringWidth(line[:i])
				if column >= 0 && at != column {
					t.Fatalf("device columns do not line up:\n%s", out)
				}
				column = at
			}
		}
	}
	if column < 0 {
		t.Fatal("no device rows")
	}
}

func TestRenderNetworkGuidesAnUnsetDevice(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		device networkDeviceView
		want   string
	}{
		{networkDeviceView{Name: "laptop", This: true}, "jara device allow"},
		{networkDeviceView{Name: "laptop", This: true, Callable: true}, "jara daemon install"},
		{networkDeviceView{Name: "laptop", This: true, Callable: true, Daemon: true}, "not linked"},
	} {
		out := renderNetwork(networkView{Here: "laptop", Devices: []networkDeviceView{tc.device}}, now, networkPainter{})
		if !strings.Contains(out, tc.want) || !strings.Contains(out, "None yet") || !strings.Contains(out, "None running") {
			t.Errorf("%+v: missing %q in:\n%s", tc.device, tc.want, out)
		}
	}
}

func TestHostRemembersDevicesThatWentOffline(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	if err := os.MkdirAll(deviceHubDir(), 0700); err != nil {
		t.Fatal(err)
	}
	seen := time.Now().Add(-time.Hour).Unix()
	if err := fsutil.WriteJSON(deviceRecordPath("desktop"), deviceRecord{Name: "desktop", Link: "x", LastSeen: seen, Closed: true}); err != nil {
		t.Fatal(err)
	}
	if record, _ := loadDeviceRecord("desktop"); record.online() {
		t.Fatal("a device whose link closed is online")
	}
	if err := os.MkdirAll(filepath.Join(networkDir(), "devices"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(networkDir(), "devices", "phone.pub"), []byte("ssh-ed25519 AAAA\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status := hubNetworkStatus()
	if status.Version != Version || len(status.Devices) != 2 {
		t.Fatalf("status = %+v", status)
	}
	desktop, phone := status.Devices[0], status.Devices[1]
	if desktop.Name != "desktop" || desktop.Online || desktop.LastSeen != seen || desktop.Joined {
		t.Fatalf("desktop = %+v", desktop)
	}
	if phone.Name != "phone" || !phone.Joined || phone.LastSeen != 0 {
		t.Fatalf("phone = %+v", phone)
	}
}
