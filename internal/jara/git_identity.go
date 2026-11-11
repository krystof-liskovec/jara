package jara

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

const (
	gitIdentityNameLimit  = 256
	gitIdentityEmailLimit = 320
)

type gitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func readGitIdentity() (gitIdentity, error) {
	name, err := readGlobalGitValue("user.name")
	if err != nil {
		return gitIdentity{}, err
	}
	email, err := readGlobalGitValue("user.email")
	if err != nil {
		return gitIdentity{}, err
	}
	return gitIdentity{Name: name, Email: email}, nil
}

func setGitIdentity(identity gitIdentity) error {
	if err := validateGitIdentity(identity); err != nil {
		return err
	}
	path, err := gitIdentityConfigPath()
	if err != nil {
		return fmt.Errorf("resolve Jara Git identity file: %w", err)
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create Jara Git identity directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".git-identity-*.tmp")
	if err != nil {
		return fmt.Errorf("prepare Jara Git identity file: %w", err)
	}
	tempPath := temp.Name()
	if err = temp.Chmod(0600); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("secure Jara Git identity file: %w", err)
	}
	if err = temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("prepare Jara Git identity file: %w", err)
	}
	defer os.Remove(tempPath)
	if err = writeGitIdentityConfig(tempPath, identity); err != nil {
		return err
	}
	if err = os.Chmod(tempPath, 0600); err != nil {
		return fmt.Errorf("secure Jara Git identity file: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install Jara Git identity file: %w", err)
	}
	if err = removeGitIdentityInclude(path); err != nil {
		return err
	}
	if err = gitConfigGlobal("--add", "include.path", path); err != nil {
		return fmt.Errorf("configure Jara Git identity include: %w", err)
	}
	return nil
}

func clearGitIdentity() error {
	path, err := gitIdentityConfigPath()
	if err != nil {
		return fmt.Errorf("resolve Jara Git identity file: %w", err)
	}
	if err = removeGitIdentityInclude(path); err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Jara Git identity file: %w", err)
	}
	return nil
}

func validateGitIdentity(identity gitIdentity) error {
	if err := validateGitIdentityField("name", identity.Name, gitIdentityNameLimit); err != nil {
		return err
	}
	if err := validateGitIdentityField("email", identity.Email, gitIdentityEmailLimit); err != nil {
		return err
	}
	if !strings.Contains(identity.Email, "@") {
		return fmt.Errorf("Git identity email must contain @")
	}
	return nil
}

func validateGitIdentityField(label, value string, limit int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("Git identity %s is required", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("Git identity %s must be valid UTF-8", label)
	}
	if len(value) > limit {
		return fmt.Errorf("Git identity %s must be at most %d bytes", label, limit)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("Git identity %s cannot contain control characters", label)
		}
	}
	return nil
}

func readGlobalGitValue(key string) (string, error) {
	cmd := exec.Command("git", "config", "--global", "--includes", "--get", key)
	output, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("read effective global Git %s", key)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func writeGitIdentityConfig(path string, identity gitIdentity) error {
	for _, pair := range [][2]string{{"user.name", identity.Name}, {"user.email", identity.Email}} {
		cmd := exec.Command("git", "config", "--file", path, pair[0], pair[1])
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("write Jara Git identity %s", pair[0])
		}
	}
	return nil
}

func gitIdentityConfigPath() (string, error) {
	return filepath.Abs(filepath.Join(fsutil.DataRoot(), "git-identity.gitconfig"))
}

func removeGitIdentityInclude(path string) error {
	cmd := exec.Command("git", "config", "--global", "--fixed-value", "--unset-all", "include.path", path)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 5 {
			return nil
		}
		return fmt.Errorf("remove Jara Git identity include")
	}
	return nil
}

func gitConfigGlobal(args ...string) error {
	cmd := exec.Command("git", append([]string{"config", "--global"}, args...)...)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
