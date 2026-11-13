//go:build linux

package jara

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeferredUpdateListsBlockersAndForce(t *testing.T) {
	x := newUpdateFixture(t)
	var log bytes.Buffer
	x.u.log = &log
	x.busy = true
	if err := x.u.update("", false); err != nil {
		t.Fatal(err)
	}
	out := log.String()
	for _, want := range []string{"Deferred: active session", "  - terminal session jara-demo (attached)", "--force"} {
		if !strings.Contains(out, want) {
			t.Fatalf("deferral output lacks %q:\n%s", want, out)
		}
	}
	if x.installs != 0 {
		t.Fatal("deferred update replaced the container")
	}
}

func TestDeferredAfterBuildListsBlockersAndForce(t *testing.T) {
	x := newUpdateFixture(t)
	var log bytes.Buffer
	x.u.log = &log
	x.lateBusy = true
	if err := x.u.update("", false); err != nil {
		t.Fatal(err)
	}
	out := log.String()
	for _, want := range []string{"Deferred after build: session opened during build", "  - SSH connection by alice running bash", "--force"} {
		if !strings.Contains(out, want) {
			t.Fatalf("deferral output lacks %q:\n%s", want, out)
		}
	}
	if x.installs != 0 {
		t.Fatal("deferred update replaced the container")
	}
}

func TestForcedUpdateReplacesBusyEnvironment(t *testing.T) {
	x := newUpdateFixture(t)
	var log bytes.Buffer
	x.u.log, x.u.Force = &log, true
	x.busy = true
	if err := x.u.update("", false); err != nil {
		t.Fatal(err)
	}
	if x.installs != 1 || !x.gate || !x.ended {
		t.Fatalf("forced update incomplete: installs=%d gate=%v ended=%v", x.installs, x.gate, x.ended)
	}
	out := log.String()
	for _, want := range []string{"WARNING: updating anyway (--force)", "terminal session jara-demo", "now interrupts", "SSH connection by alice"} {
		if !strings.Contains(out, want) {
			t.Fatalf("forced update output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Re-run with --force") {
		t.Fatalf("forced update suggested --force:\n%s", out)
	}
}

func TestForcedUpdateWithOlderServerSkipsGate(t *testing.T) {
	x := newUpdateFixture(t)
	var log bytes.Buffer
	x.u.log, x.u.Force = &log, true
	x.busy, x.oldServer = true, true
	if err := x.u.update("", false); err != nil {
		t.Fatal(err)
	}
	if x.installs != 1 {
		t.Fatal("forced update stopped at an older server's gate")
	}
	if !strings.Contains(log.String(), "cannot take a forced update gate") {
		t.Fatalf("missing older-server warning:\n%s", log.String())
	}
}
