package jara

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/fsutil"
)

func askTestDevice(t *testing.T, request deviceRequest) string {
	t.Helper()
	client, server := net.Pipe()
	go handleDeviceRequest(server)
	defer client.Close()
	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func TestDeviceAnswersOnlyWhenAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("JARA_DATA", filepath.Join(home, "data"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteJSON(localRunnerMarkerPath(localWorkspaceID(project)), localRunnerMarker{Root: project}); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteJSON(localRunnerMarkerPath("local-gone"), localRunnerMarker{Root: filepath.Join(home, "deleted")}); err != nil {
		t.Fatal(err)
	}
	if answer := askTestDevice(t, deviceRequest{Op: "folders"}); !strings.Contains(answer, "jara device allow") {
		t.Fatalf("a device that was not allowed answered: %s", answer)
	}
	if err := saveConfig(Config{DeviceCallable: true}); err != nil {
		t.Fatal(err)
	}
	var folders []deviceFolder
	if err := json.Unmarshal([]byte(askTestDevice(t, deviceRequest{Op: "folders"})), &folders); err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Folder != project || folders[0].Running {
		t.Fatalf("folders = %+v", folders)
	}
	answer := askTestDevice(t, deviceRequest{Op: "open", Folder: filepath.Join(home, "elsewhere")})
	if !strings.Contains(answer, "jara/error") || !strings.Contains(answer, "has opened with Jara") {
		t.Fatalf("opened a folder the device never opened: %s", answer)
	}
}

func TestDeviceSessionTargets(t *testing.T) {
	t.Setenv("JARA_DEVICE", "desk")
	session := liveSession{Name: "api", Runner: "laptop", Remote: "work", DeviceFolder: "/src/api"}
	if got := session.where(); got != "laptop (via work)" {
		t.Fatalf("where = %q", got)
	}
	if got := session.target(); got != "laptop:/src/api" {
		t.Fatalf("target = %q", got)
	}
	got, err := chooseLiveSession([]liveSession{session}, nil, "laptop:/src/api", strings.NewReader(""))
	if err != nil || got.DeviceFolder != "/src/api" {
		t.Fatalf("attach by target = %+v %v", got, err)
	}
}
