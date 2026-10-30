package jara

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestRemoteShellArgumentsAreLiteral(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip(err)
	}
	value := "space ' quote; $(printf injected) `printf injected`\nline"
	b, err := exec.Command("sh", "-c", shellArgs("printf", "%s", value)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != value {
		t.Fatalf("argument was evaluated: %q", b)
	}
}
func TestRequestRoundTrip(t *testing.T) {
	want := Request{ID: "w-one", Resume: true, Subdir: "a b"}
	var got Request
	if err := decode(token(want), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("request changed")
	}
}
func TestSSHNeverDisablesHostVerification(t *testing.T) {
	a := strings.Join(sshArgs(Remote{Host: "example"}, true), " ")
	if !strings.Contains(a, "StrictHostKeyChecking=ask") || !strings.Contains(a, "-tt") {
		t.Fatal(a)
	}
}
