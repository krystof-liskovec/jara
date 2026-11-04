package jara

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitIdentityRPCRoundTripAndInvalidInput(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("JARA_DATA", t.TempDir())
	var output bytes.Buffer
	input := `{"name":"Test Author","email":"author@example.com"}`
	if err := gitIdentityRPC("git-identity-set", strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`null`, `{}`, `{"name":"Only name"}`, input + `{}`, strings.Repeat("x", 8193)} {
		if err := gitIdentityRPC("git-identity-set", strings.NewReader(invalid), &output); err == nil {
			t.Fatal("invalid identity was accepted")
		}
	}
	if err := gitIdentityRPC("git-identity-show", nil, &output); err != nil {
		t.Fatal(err)
	}
	var identity gitIdentity
	if err := json.Unmarshal(output.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.Name != "Test Author" || identity.Email != "author@example.com" {
		t.Fatalf("invalid input changed the saved identity: %+v", identity)
	}
	if err := gitIdentityRPC("git-identity-clear", nil, &output); err != nil {
		t.Fatal(err)
	}
	identity, err := readGitIdentity()
	if err != nil || identity.Name != "" || identity.Email != "" {
		t.Fatalf("clear failed: %+v, %v", identity, err)
	}
}
