package jara

import (
	"bufio"
	"strings"
	"testing"
)

func TestDeploymentAttachmentValidation(t *testing.T) {
	for _, test := range []struct {
		name, directory string
		manifest        nativeDeployment
		valid           bool
	}{
		{"work", ".jara-deploy-work", newNativeDeployment("work", "0.0.0.0", 2222), true},
		{"work", "jara-deploy-work", newNativeDeployment("work", "0.0.0.0", 2222), true},
		{"work", "../.jara-deploy-work", newNativeDeployment("work", "0.0.0.0", 2222), false},
		{"work", ".jara-deploy-work", newNativeDeployment("other", "0.0.0.0", 2222), false},
		{"work", ".jara-deploy-work", newNativeDeployment("work", "0.0.0.0", 0), false},
	} {
		err := validateDeploymentAttachment(test.name, test.directory, test.manifest)
		if (err == nil) != test.valid {
			t.Errorf("directory %q, manifest %+v: %v", test.directory, test.manifest, err)
		}
	}
}

func TestDeploymentAttachmentChecksServerIdentity(t *testing.T) {
	for _, test := range []struct {
		left, right string
		match       bool
	}{
		{"ssh-ed25519 AAAA container", "ssh-ed25519 AAAA server\n", true},
		{"ssh-ed25519 AAAA", "ssh-ed25519 BBBB", false},
		{"", "", false},
		{"ssh-ed25519", "ssh-ed25519", false},
		{"ssh-rsa AAAA", "ssh-rsa AAAA", false},
	} {
		if got := sameDeploymentHostKey([]byte(test.left), []byte(test.right)); got != test.match {
			t.Errorf("sameDeploymentHostKey(%q, %q) = %v", test.left, test.right, got)
		}
	}
}

func TestDeploymentAdminReusesKeyWithoutServicePortOrUser(t *testing.T) {
	service := Remote{Host: "server", User: "agent", Port: 2222, Identity: "/keys/jara"}
	admin, err := askDeploymentAdmin(bufio.NewReader(strings.NewReader("\n\n\ny\n")), service)
	if err != nil {
		t.Fatal(err)
	}
	if admin != (Remote{Host: "server", Identity: "/keys/jara"}) {
		t.Fatalf("unexpected admin connection: %+v", admin)
	}
	if service.User != "agent" || service.Port != 2222 {
		t.Fatal("service connection was changed")
	}
}

func TestDeploymentAdminRejectsInvalidPort(t *testing.T) {
	_, err := askDeploymentAdmin(bufio.NewReader(strings.NewReader("server\nuser\n70000\n")), Remote{})
	if err == nil {
		t.Fatal("accepted invalid host port")
	}
}

func TestDeploymentNamesPreserveOriginalIdentity(t *testing.T) {
	if got := deploymentName("jellyfish", HostDeployment{Name: "work"}); got != "work" {
		t.Fatalf("attached deployment name = %q", got)
	}
	if got := deploymentName("jellyfish", HostDeployment{}); got != "jellyfish" {
		t.Fatalf("legacy deployment name = %q", got)
	}
}

func TestUpgradeExplainsMissingHostConnection(t *testing.T) {
	if len(deploymentBundle()) == 0 {
		t.Skip("client deployment bundle required")
	}
	c := Config{Default: "jellyfish", Remotes: map[string]Remote{"jellyfish": {Host: "server"}}}
	for _, args := range [][]string{nil, {"jellyfish"}} {
		err := deploymentUpgradeCommand(c, args)
		if err == nil || !strings.Contains(err.Error(), "run jara setup") ||
			!strings.Contains(err.Error(), "enable host management") ||
			strings.Contains(err.Error(), "was not deployed") {
			t.Fatalf("upgrade %v returned misleading error: %v", args, err)
		}
	}
}
