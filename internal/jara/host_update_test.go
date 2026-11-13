//go:build linux

package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseComparisonAndPins(t *testing.T) {
	for _, s := range []string{"latest", "0.166.0-alpha.1", "1.2.3;evil", "1.2.3\n", "01.2.3"} {
		if _, err := parseRelease(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	newer, err := newerRelease("0.166.0", "0.99.0")
	if err != nil || !newer {
		t.Fatal("numeric version comparison failed")
	}
	newer, err = newerRelease("0.165.0", "0.166.0")
	if err != nil || newer {
		t.Fatal("downgrade permitted")
	}
	data := []byte("# config\nCODEX_VERSION='0.165.0' # old\nOTHER=keep\n")
	version, err := codexPin(data)
	if err != nil || version != "0.165.0" {
		t.Fatal(err)
	}
	updated, err := replaceCodexPin(data, "0.166.0")
	if err != nil || !bytes.Contains(updated, []byte("OTHER=keep")) {
		t.Fatal(err)
	}
	if _, err = replaceCodexPin([]byte("CODEX_VERSION=0.1.0\nCODEX_VERSION=0.2.0\n"), "0.166.0"); err == nil {
		t.Fatal("ambiguous pin accepted")
	}
}

func TestEnvPinRefusesConcurrentChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	old := []byte("CODEX_VERSION=0.165.0\n")
	os.WriteFile(p, []byte("CODEX_VERSION=0.164.0\n"), 0600)
	if writeEnvPin(p, old, []byte("CODEX_VERSION=0.166.0\n")) == nil {
		t.Fatal("concurrent env edit was lost")
	}
}

type updateFixture struct {
	u                                     hostUpdater
	commands                              []string
	busy, lateBusy, smokeFail, deployFail bool
	gate, ended                           bool
	oldServer                             bool // the running Jara predates --force
	version                               string
	installs                              int
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	os.WriteFile(p, []byte("CODEX_VERSION=0.165.0\nKEEP=untouched\n"), 0600)
	x := &updateFixture{version: "0.165.0"}
	x.u = hostUpdater{Project: "test", Compose: filepath.Join(dir, "compose.yaml"), EnvFile: p, latest: func() (string, error) { return "0.166.0", nil }, log: io.Discard}
	os.WriteFile(x.u.Compose, []byte("services: {}\n"), 0600)
	x.u.run = func(timeout time.Duration, a []string, stream bool) ([]byte, error) {
		cmd := strings.Join(a, " ")
		x.commands = append(x.commands, cmd)
		switch {
		case strings.Contains(cmd, " ps -q agent"):
			return []byte(strings.Repeat("a", 64)), nil
		case strings.HasSuffix(cmd, "codex --version"):
			return []byte("codex-cli " + x.version), nil
		case strings.Contains(cmd, "_maintenance status"):
			if !x.busy {
				return []byte(`{"Busy":false}`), nil
			}
			return json.Marshal(maintenanceStatus{Busy: true, Reason: "active session", Blockers: []string{"terminal session jara-demo (attached)"}})
		case strings.Contains(cmd, "_maintenance begin"):
			forced := strings.HasSuffix(cmd, " force")
			if forced && x.oldServer {
				return nil, fmt.Errorf("invalid maintenance invocation")
			}
			if x.lateBusy && !forced {
				return json.Marshal(maintenanceStatus{Busy: true, Reason: "session opened during build", Blockers: []string{"SSH connection by alice running bash"}})
			}
			x.gate = true
			if forced {
				return json.Marshal(maintenanceStatus{Busy: x.busy || x.lateBusy, Forced: true, Blockers: []string{"SSH connection by alice running bash"}})
			}
			return []byte(`{"Busy":false}`), nil
		case strings.Contains(cmd, "_maintenance end"):
			x.ended = true
			return nil, nil
		case strings.Contains(cmd, "config --format json"):
			return []byte(`{"services":{"agent":{"security_opt":["seccomp=./seccomp/codex.json"]}}}`), nil
		case strings.Contains(cmd, " build agent"):
			return nil, nil
		case strings.Contains(cmd, "docker run"):
			if x.smokeFail {
				return nil, fmt.Errorf("candidate failed")
			}
			if strings.Contains(cmd, "--volume") || strings.Contains(cmd, "--mount") {
				t.Fatal("candidate mounted live state")
			}
			return nil, nil
		case strings.Contains(cmd, "{{json .Config.Env}}"):
			return []byte(`[]`), nil
		case strings.Contains(cmd, "docker inspect"):
			return []byte("sha256:" + strings.Repeat("b", 64)), nil
		case strings.Contains(cmd, "docker tag"):
			return nil, nil
		case strings.Contains(cmd, " up -d"):
			if !x.gate && !x.oldServer {
				t.Fatal("replaced container without gate")
			}
			x.installs++
			if x.installs == 1 && x.deployFail {
				return nil, fmt.Errorf("deployment failed")
			}
			if x.installs == 1 {
				x.version = "0.166.0"
			} else {
				x.version = "0.165.0"
			}
			return nil, nil
		case strings.Contains(cmd, "jara _rpc"):
			return json.Marshal(Diagnosis{Protocol: Protocol, OK: true})
		default:
			return nil, fmt.Errorf("unexpected command %s", cmd)
		}
	}
	return x
}
func TestUpdaterDefersWhileSessionsExist(t *testing.T) {
	x := newUpdateFixture(t)
	x.busy = true
	if err := x.u.update("", false); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range x.commands {
		if strings.Contains(cmd, "build agent") || strings.Contains(cmd, "up -d") {
			t.Fatal("busy environment changed")
		}
	}
}
func TestUpdaterRechecksAfterBuild(t *testing.T) {
	x := newUpdateFixture(t)
	x.lateBusy = true
	if err := x.u.update("", false); err != nil {
		t.Fatal(err)
	}
	if x.installs != 0 {
		t.Fatal("new session interrupted")
	}
}
func TestUpdaterSmokeFailureLeavesDeploymentAndPin(t *testing.T) {
	x := newUpdateFixture(t)
	x.smokeFail = true
	if err := x.u.update("", false); err == nil {
		t.Fatal("failure swallowed")
	}
	if x.installs != 0 {
		t.Fatal("deployed failed candidate")
	}
	data, _ := os.ReadFile(x.u.EnvFile)
	if !bytes.Contains(data, []byte("0.165.0")) {
		t.Fatal("pin changed")
	}
}
func TestUpdaterCommitsPinAfterHealthyDeployment(t *testing.T) {
	x := newUpdateFixture(t)
	if err := x.u.update("", false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(x.u.EnvFile)
	if !bytes.Contains(data, []byte("CODEX_VERSION=0.166.0\nKEEP=untouched")) || !x.ended || x.installs != 1 {
		t.Fatalf("update incomplete: %s", data)
	}
}
func TestUpdaterRestoresPreviousImageOnDeploymentFailure(t *testing.T) {
	x := newUpdateFixture(t)
	x.deployFail = true
	if err := x.u.update("", false); err == nil || !strings.Contains(err.Error(), "previous image restored") {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(x.u.EnvFile)
	if !bytes.Contains(data, []byte("0.165.0")) || !x.ended || x.installs != 2 {
		t.Fatal("rollback incomplete")
	}
}
func TestUpdateCheckDoesNotBuildOrTakeGate(t *testing.T) {
	x := newUpdateFixture(t)
	if err := x.u.update("", true); err != nil {
		t.Fatal(err)
	}
	if x.gate || x.installs != 0 {
		t.Fatal("check mutated deployment")
	}
}

func TestUpdaterRefusesConfigChangesDuringBuild(t *testing.T) {
	for _, which := range []string{"env", "compose"} {
		t.Run(which, func(t *testing.T) {
			x := newUpdateFixture(t)
			previous := x.u.run
			x.u.run = func(timeout time.Duration, a []string, stream bool) ([]byte, error) {
				if strings.Contains(strings.Join(a, " "), " build agent") {
					p := x.u.EnvFile
					if which == "compose" {
						p = x.u.Compose
					}
					if err := os.WriteFile(p, []byte("changed by operator\n"), 0600); err != nil {
						return nil, err
					}
				}
				return previous(timeout, a, stream)
			}
			if err := x.u.update("", false); err == nil || !strings.Contains(err.Error(), "changed during build") {
				t.Fatal(err)
			}
			if x.installs != 0 || x.gate {
				t.Fatal("updated deployment after concurrent config change")
			}
		})
	}
}

func TestUpdateRetainsComposeOverlay(t *testing.T) {
	x := newUpdateFixture(t)
	x.u.ExtraCompose = filepath.Join(filepath.Dir(x.u.Compose), "docker.yaml")
	os.WriteFile(x.u.ExtraCompose, []byte("services: {}\n"), 0600)
	if err := x.u.update("0.166.0", false); err != nil {
		t.Fatal(err)
	}
	for _, command := range x.commands {
		if strings.HasPrefix(command, "docker compose ") && !strings.Contains(command, "--file "+x.u.ExtraCompose) {
			t.Fatalf("overlay lost: %s", command)
		}
	}
}

func TestUpdateRefusesLostDockerOverlay(t *testing.T) {
	x := newUpdateFixture(t)
	run := x.u.run
	x.u.run = func(timeout time.Duration, args []string, stream bool) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "{{json .Config.Env}}") {
			return []byte(`["JARA_DOCKER=1"]`), nil
		}
		return run(timeout, args, stream)
	}
	if err := x.u.update("0.166.0", false); err == nil || !strings.Contains(err.Error(), "--compose-extra") {
		t.Fatalf("missing overlay not diagnosed: %v", err)
	}
	if x.installs != 0 {
		t.Fatal("deployment changed")
	}
}

func TestUpdateRefusesChangedOverlay(t *testing.T) {
	x := newUpdateFixture(t)
	x.u.ExtraCompose = filepath.Join(filepath.Dir(x.u.Compose), "docker.yaml")
	os.WriteFile(x.u.ExtraCompose, []byte("services: {}\n"), 0600)
	run := x.u.run
	x.u.run = func(timeout time.Duration, args []string, stream bool) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), " build agent") {
			os.WriteFile(x.u.ExtraCompose, []byte("changed"), 0600)
		}
		return run(timeout, args, stream)
	}
	if err := x.u.update("0.166.0", false); err == nil || !strings.Contains(err.Error(), "overlay changed") {
		t.Fatalf("changed overlay not diagnosed: %v", err)
	}
	if x.installs != 0 {
		t.Fatal("deployment changed")
	}
}
