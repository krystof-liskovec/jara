package jara

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitIdentityIncludeCommitAndClear(t *testing.T) {
	_, data := isolateGitIdentity(t)
	other := filepath.Join(data, "other.gitconfig")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	gitIdentityTestCommand(t, "", "config", "--file", other, "alias.keep", "status --short")
	gitIdentityTestCommand(t, "", "config", "--global", "user.name", "Prior Name")
	gitIdentityTestCommand(t, "", "config", "--global", "user.email", "prior@example.invalid")
	gitIdentityTestCommand(t, "", "config", "--global", "--add", "include.path", other)
	gitIdentityTestCommand(t, "", "config", "--global", "credential.helper", "store --file=private-credentials")
	gitIdentityTestCommand(t, "", "config", "--global", "credential.https://git.example.invalid.username", "preserve-user")

	marker := filepath.Join(data, "shell-was-run")
	identity := gitIdentity{Name: fmt.Sprintf("Coder $(touch %s) \"literal\"", marker), Email: "coder+tag@example.invalid"}
	if err := setGitIdentity(identity); err != nil {
		t.Fatal(err)
	}
	if err := setGitIdentity(identity); err != nil {
		t.Fatalf("replacing existing Jara identity: %v", err)
	}
	got, err := readGitIdentity()
	if err != nil || got != identity {
		t.Fatalf("global identity = %+v, err=%v", got, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("identity value executed a shell command: stat err=%v", err)
	}
	identityPath, err := gitIdentityConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(identityPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("managed identity file mode is not private: info=%v err=%v", info, err)
	}

	repo := filepath.Join(data, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitIdentityTestCommand(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "readme.txt"), []byte("identity test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIdentityTestCommand(t, repo, "config", "user.name", "Local Override")
	gitIdentityTestCommand(t, repo, "config", "user.email", "local@example.invalid")
	t.Chdir(repo)
	got, err = readGitIdentity()
	if err != nil || got != identity {
		t.Fatalf("repo-local identity affected global read: %+v err=%v", got, err)
	}
	gitIdentityTestCommand(t, repo, "config", "--local", "--unset-all", "user.name")
	gitIdentityTestCommand(t, repo, "config", "--local", "--unset-all", "user.email")
	commitIdentity := gitIdentity{Name: "Jara Étude", Email: "jara@example.invalid"}
	if err := setGitIdentity(commitIdentity); err != nil {
		t.Fatal(err)
	}
	gitIdentityTestCommand(t, repo, "add", "readme.txt")
	gitIdentityTestCommand(t, repo, "-c", "commit.gpgsign=false", "commit", "--no-gpg-sign", "-m", "identity test")
	commit := gitIdentityTestCommand(t, repo, "log", "-1", "--format=%an%x00%ae")
	name, email, ok := strings.Cut(strings.TrimSuffix(string(commit), "\n"), "\x00")
	if !ok || name != commitIdentity.Name || email != commitIdentity.Email {
		t.Fatalf("commit identity did not use Jara configuration: name matches=%v email matches=%v", name == commitIdentity.Name, email == commitIdentity.Email)
	}

	if err := clearGitIdentity(); err != nil {
		t.Fatal(err)
	}
	got, err = readGitIdentity()
	if err != nil || got != (gitIdentity{Name: "Prior Name", Email: "prior@example.invalid"}) {
		t.Fatalf("clearing Jara identity did not restore previous globals: %+v err=%v", got, err)
	}
	if _, err := os.Stat(identityPath); !os.IsNotExist(err) {
		t.Fatalf("managed identity file remains after clear: %v", err)
	}
	if value := gitIdentityTestCommand(t, "", "config", "--global", "--includes", "--get", "alias.keep"); strings.TrimSpace(string(value)) != "status --short" {
		t.Fatalf("unrelated include was not preserved: %q", value)
	}
	if value := gitIdentityTestCommand(t, "", "config", "--global", "--get", "credential.helper"); strings.TrimSpace(string(value)) != "store --file=private-credentials" {
		t.Fatalf("credential helper changed: %q", value)
	}
	if value := gitIdentityTestCommand(t, "", "config", "--global", "--get", "credential.https://git.example.invalid.username"); strings.TrimSpace(string(value)) != "preserve-user" {
		t.Fatalf("credential configuration changed: %q", value)
	}
	if err := clearGitIdentity(); err != nil {
		t.Fatalf("clearing absent identity should be safe: %v", err)
	}
}

func TestGitIdentityInvalidInputDoesNotMutateConfig(t *testing.T) {
	global, _ := isolateGitIdentity(t)
	identity := gitIdentity{Name: "Valid Name", Email: "valid@example.invalid"}
	if err := setGitIdentity(identity); err != nil {
		t.Fatal(err)
	}
	identityPath, err := gitIdentityConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	globalBefore, err := os.ReadFile(global)
	if err != nil {
		t.Fatal(err)
	}
	identityBefore, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []gitIdentity{
		{Email: "valid@example.invalid"},
		{Name: "Valid Name"},
		{Name: "Name\nOther", Email: "valid@example.invalid"},
		{Name: "Valid Name", Email: "invalid-without-at"},
		{Name: "Valid Name", Email: "bad\x00@example.invalid"},
		{Name: strings.Repeat("n", gitIdentityNameLimit+1), Email: "valid@example.invalid"},
	}
	for _, candidate := range invalid {
		if err := setGitIdentity(candidate); err == nil {
			t.Fatalf("accepted invalid identity with name length %d", len(candidate.Name))
		}
		globalAfter, err := os.ReadFile(global)
		if err != nil || !bytes.Equal(globalBefore, globalAfter) {
			t.Fatalf("invalid identity mutated global Git config; read err=%v", err)
		}
		identityAfter, err := os.ReadFile(identityPath)
		if err != nil || !bytes.Equal(identityBefore, identityAfter) {
			t.Fatalf("invalid identity mutated managed file; read err=%v", err)
		}
	}
	got, err := readGitIdentity()
	if err != nil || got != identity {
		t.Fatalf("invalid input changed effective identity: %+v err=%v", got, err)
	}
}

func TestReadGitIdentityUsesGlobalValuesOnly(t *testing.T) {
	_, _ = isolateGitIdentity(t)
	gitIdentityTestCommand(t, "", "config", "--global", "user.name", "Global User")
	gitIdentityTestCommand(t, "", "config", "--global", "user.email", "global@example.invalid")
	repo := t.TempDir()
	gitIdentityTestCommand(t, repo, "init", "-q")
	gitIdentityTestCommand(t, repo, "config", "user.name", "Repository User")
	gitIdentityTestCommand(t, repo, "config", "user.email", "repository@example.invalid")
	t.Chdir(repo)
	got, err := readGitIdentity()
	if err != nil || got != (gitIdentity{Name: "Global User", Email: "global@example.invalid"}) {
		t.Fatalf("identity read included repository-local values: %+v err=%v", got, err)
	}
}

func isolateGitIdentity(t *testing.T) (global, data string) {
	t.Helper()
	root := t.TempDir()
	global = filepath.Join(root, "global.gitconfig")
	data = filepath.Join(root, "jara-data")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("JARA_DATA", data)
	return global, data
}

func gitIdentityTestCommand(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v", args[0], err)
	}
	return output
}
