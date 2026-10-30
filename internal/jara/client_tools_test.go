package jara

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePortSpecs(t *testing.T) {
	got, err := parsePortSpecs([]string{"3000,8000-8002", "9000"})
	if err != nil {
		t.Fatal(err)
	}
	want := []int{3000, 8000, 8001, 8002, 9000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePortSpecs() = %v, want %v", got, want)
	}
}

func TestParsePortSpecsRejectsInvalidLists(t *testing.T) {
	for _, spec := range [][]string{
		{"0"}, {"65536"}, {"100-99"}, {"1,,2"}, {"2,2"}, {"1-257"}, {"1", "1"},
	} {
		if _, err := parsePortSpecs(spec); err == nil {
			t.Errorf("parsePortSpecs(%q) unexpectedly succeeded", spec)
		}
	}
}

func TestTunnelArgsCombineForwardAndReverseInOneSSHConnection(t *testing.T) {
	args, err := tunnelArgs(Remote{Host: "jara-box"}, []int{3000, 3001}, []int{5432}, "", "", "127.0.0.1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-L 127.0.0.1:3000:127.0.0.1:3000",
		"-L 127.0.0.1:3001:127.0.0.1:3001",
		"-R 127.0.0.1:5432:127.0.0.1:5432",
		"-- jara-box",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("tunnel args %q do not contain %q", joined, want)
		}
	}
}

func TestTunnelArgsUseSeparateTargetsAndBindAddresses(t *testing.T) {
	args, err := tunnelArgs(Remote{Host: "jara-box"}, []int{8080}, []int{15432}, "127.0.0.1:3000", "127.0.0.1:5432", "0.0.0.0", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-L 0.0.0.0:8080:127.0.0.1:3000", "-R 127.0.0.1:15432:127.0.0.1:5432"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tunnel args %q do not contain %q", joined, want)
		}
	}
}
