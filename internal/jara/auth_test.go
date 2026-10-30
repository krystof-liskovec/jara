package jara

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTokenUsesStdinAndRepeatedSetupIsIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	t.Setenv("JARA_DATA", filepath.Join(dir, "data"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE", filepath.Join(dir, "capture"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$CAPTURE.args\"\ncat > \"$CAPTURE.token\"\nprintf called >> \"$CAPTURE.calls\"\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	defer func() { os.Stdin = original }()
	for i := 0; i < 2; i++ {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		write.WriteString("dummy-test-token\n")
		write.Close()
		os.Stdin = read
		err = remoteAuth("gitlab", "gitlab.example.org")
		read.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	args, err := os.ReadFile(filepath.Join(dir, "capture.args"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "dummy-test-token") {
		t.Fatal("token entered command arguments")
	}
	token, _ := os.ReadFile(filepath.Join(dir, "capture.token"))
	if string(token) != "dummy-test-token" {
		t.Fatal("token not passed through stdin")
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "capture.calls"))
	if string(calls) != "called" {
		t.Fatal("startup was not idempotent")
	}
}

func TestProviderRejectsCredentialURLsAndMalformedHosts(t *testing.T) {
	for _, url := range []string{"http://gitea.example.org", "https://user:secret@gitea.example.org", "https://gitea.example.org?token=secret"} {
		if validateProvider("gitea", url) == nil {
			t.Errorf("accepted %s", url)
		}
	}
	if validateProvider("gitlab", "host\nother") == nil {
		t.Fatal("accepted newline")
	}
}
