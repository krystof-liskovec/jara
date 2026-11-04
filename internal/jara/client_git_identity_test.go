package jara

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func installGitIdentityFakeSSH(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SSH shim uses a POSIX shell")
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$JARA_TEST_SSH_ARGS\"\ncat > \"$JARA_TEST_SSH_STDIN\"\nif [ -n \"$JARA_TEST_SSH_OUTPUT\" ]; then cat \"$JARA_TEST_SSH_OUTPUT\"; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("JARA_TEST_SSH_ARGS", filepath.Join(dir, "ssh-args"))
	t.Setenv("JARA_TEST_SSH_STDIN", filepath.Join(dir, "ssh-stdin"))
	t.Setenv("JARA_TEST_SSH_OUTPUT", "")
}

func fakeSSHRequest(t *testing.T, path string) Request {
	t.Helper()
	args, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.TrimSpace(string(args)))
	if len(fields) < 3 {
		t.Fatalf("fake SSH did not receive an _rpc command: %q", args)
	}
	tokenValue := strings.Trim(fields[len(fields)-1], "'")
	var request Request
	if err := decode(tokenValue, &request); err != nil {
		t.Fatalf("decode remote request token: %v; command args=%q", err, args)
	}
	return request
}

func captureGitIdentityStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	callErr := fn()
	_ = write.Close()
	os.Stdout = previous
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(output), callErr
}

func TestGitIdentityConfigRemoteSetUsesSelectedRemoteAndJSONStdin(t *testing.T) {
	if testing.Short() {
		t.Skip("uses a local SSH command shim")
	}
	dir := t.TempDir()
	installGitIdentityFakeSSH(t, dir)
	config := Config{
		Default: "primary",
		Remotes: map[string]Remote{
			"primary":   {Host: "primary.example"},
			"secondary": {Host: "secondary.example"},
		},
	}
	if err := configCommand(&config, []string{"git-identity", "set", "--name", "Jane Doe", "--email", "jane@example.com"}); err != nil {
		t.Fatal(err)
	}
	request := fakeSSHRequest(t, os.Getenv("JARA_TEST_SSH_ARGS"))
	if request.Op != "git-identity-set" {
		t.Fatalf("remote operation = %q", request.Op)
	}
	if !strings.Contains(string(mustRead(t, os.Getenv("JARA_TEST_SSH_ARGS"))), "primary.example") {
		t.Fatal("default remote was not selected")
	}
	var identity gitIdentity
	if err := json.Unmarshal(mustRead(t, os.Getenv("JARA_TEST_SSH_STDIN")), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.Name != "Jane Doe" || identity.Email != "jane@example.com" {
		t.Fatalf("identity stdin = %+v", identity)
	}
	if err := configCommand(&config, []string{"git-identity", "set", "--remote", "secondary", "--name", "Ada", "--email", "ada@example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := fakeSSHRequest(t, os.Getenv("JARA_TEST_SSH_ARGS")); got.Op != "git-identity-set" || !strings.Contains(string(mustRead(t, os.Getenv("JARA_TEST_SSH_ARGS"))), "secondary.example") {
		t.Fatalf("explicit remote was not selected: %+v", got)
	}
}

func TestGitIdentityConfigShowAndClearUseRemoteOperations(t *testing.T) {
	dir := t.TempDir()
	installGitIdentityFakeSSH(t, dir)
	outputFile := filepath.Join(dir, "ssh-output")
	if err := os.WriteFile(outputFile, []byte(`{"name":"Remote Name","email":"remote@example.com"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JARA_TEST_SSH_OUTPUT", outputFile)
	config := Config{Default: "work", Remotes: map[string]Remote{"work": {Host: "work.example"}}}
	output, err := captureGitIdentityStdout(t, func() error {
		return configCommand(&config, []string{"git-identity", "show"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if fakeSSHRequest(t, os.Getenv("JARA_TEST_SSH_ARGS")).Op != "git-identity-show" || !strings.Contains(output, "Remote Name") || !strings.Contains(output, "remote@example.com") {
		t.Fatalf("remote show output/operation: %q", output)
	}
	if err := configCommand(&config, []string{"git-identity", "clear"}); err != nil {
		t.Fatal(err)
	}
	if fakeSSHRequest(t, os.Getenv("JARA_TEST_SSH_ARGS")).Op != "git-identity-clear" {
		t.Fatal("clear did not send the clear operation")
	}
	if got := mustRead(t, os.Getenv("JARA_TEST_SSH_STDIN")); len(got) != 0 {
		t.Fatalf("clear should not send request payload: %q", got)
	}
}

func TestGitIdentityConfigLocalModeAndArgumentValidation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(os.Getenv("HOME"), ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("JARA_DATA", t.TempDir())
	config := Config{}
	if err := configCommand(&config, []string{"git-identity", "set", "--local", "--name", "Local Name", "--email", "local@example.com"}); err != nil {
		t.Fatal(err)
	}
	identity, err := readGitIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity.Name != "Local Name" || identity.Email != "local@example.com" {
		t.Fatalf("local identity = %+v", identity)
	}
	if err := configCommand(&config, []string{"git-identity", "show"}); err == nil || !strings.Contains(err.Error(), "configured host") {
		t.Fatalf("missing remote should require explicit --local: %v", err)
	}
	for _, args := range [][]string{
		{"git-identity", "set", "--local", "--name", "Only Name"},
		{"git-identity", "set", "--local", "--name", "Name", "--email", "mail@example.com", "extra"},
		{"git-identity", "show", "--local", "--remote", "work"},
		{"git-identity", "clear", "--name", "Name"},
	} {
		if err := configCommand(&config, args); err == nil {
			t.Errorf("invalid command accepted: %q", args)
		}
	}
	if err := configCommand(&config, []string{"git-identity", "clear", "--local"}); err != nil {
		t.Fatal(err)
	}
}

func TestGitIdentityHelpLookup(t *testing.T) {
	for _, args := range [][]string{
		{"config", "git-identity"},
		{"config", "git-identity", "show"},
		{"config", "git-identity", "set"},
		{"config", "git-identity", "clear"},
	} {
		key := commandHelpKey(args)
		if _, ok := commandHelp[key]; !ok {
			t.Errorf("missing help for %q (key %q)", args, key)
		}
	}
	if key := commandHelpKey([]string{"config", "git-identity", "set", "--local", "--help"}); key != "config git-identity set" {
		t.Fatalf("help lookup with --local = %q", key)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGitIdentityTransportJSONShape(t *testing.T) {
	var buffer bytes.Buffer
	if err := json.NewEncoder(&buffer).Encode(gitIdentity{Name: "Name", Email: "name@example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buffer.String()); got != `{"name":"Name","email":"name@example.com"}` {
		t.Fatalf("identity transport JSON = %s", got)
	}
}
