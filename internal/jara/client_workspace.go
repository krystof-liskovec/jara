package jara

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func validateOrigin(origin string) error {
	if origin == "" || strings.HasPrefix(origin, "-") || strings.ContainsAny(origin, "\x00\r\n") {
		return fmt.Errorf("invalid origin URL")
	}
	if u, err := url.Parse(origin); err == nil && (u.Scheme == "https" || u.Scheme == "http") && (u.User != nil || u.RawQuery != "") {
		return fmt.Errorf("origin must not embed credentials or query parameters; configure a remote Git credential helper")
	}
	return nil
}

func confirm(prompt string) (bool, error) {
	return confirmWithReader(bufio.NewReader(os.Stdin), prompt)
}

func confirmWithReader(reader *bufio.Reader, prompt string) (bool, error) {
	fmt.Fprint(os.Stderr, prompt+" [y/N] ")
	s, err := reader.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		return false, err
	}
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes", nil
}

func syncLocalChangesBeforeSession(r Remote, b Binding, root, remoteName string, includeIgnored, isGit bool, reader *bufio.Reader) error {
	pending, deletions, err := localPushChangesPending(b, root, includeIgnored, isGit)
	if err != nil {
		return fmt.Errorf("local changes could not be checked: %w; run `jara sync push` if needed", err)
	}
	if !pending {
		return nil
	}
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	prompt := "Local changes are not synced. Sync them to " + remoteName + " before starting?"
	if deletions {
		prompt = "Local changes are not synced. Sync them to " + remoteName + " before starting? This will also remove files deleted locally from the remote."
	}
	pushChanges, err := confirmWithReader(reader, prompt)
	if err != nil && err != io.EOF {
		return fmt.Errorf("local changes were left here: %w; run `jara sync push` when ready", err)
	}
	if err == io.EOF || !pushChanges {
		message := "Local changes were left on this machine; run `jara sync push` to transfer them later."
		if err == io.EOF {
			message = "No response; local changes were left on this machine. Run `jara sync push` to transfer them later."
		}
		fmt.Fprintln(os.Stderr, message)
		return nil
	}
	if err = syncWorkspace(r, b, root, "push", false, includeIgnored, deletions); err != nil {
		return fmt.Errorf("local changes could not be synced: %w; retry with `jara sync push`", err)
	}
	return nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}

func workspaceRoot(cwd string, copyMode bool) (string, string, bool) {
	if !copyMode {
		if root, err := gitOutput(cwd, "rev-parse", "--show-toplevel"); err == nil {
			rel, e := filepath.Rel(root, cwd)
			if e == nil {
				return root, rel, true
			}
		}
	}
	return cwd, ".", false
}

func workspaceContext(c Config, cwd string, copyMode bool) (string, string, bool) {
	root, rel, isGit := workspaceRoot(cwd, copyMode)
	if copyMode {
		return root, rel, isGit
	}
	for candidate := cwd; ; candidate = filepath.Dir(candidate) {
		if _, ok := c.Bindings[candidate]; ok {
			relative, err := filepath.Rel(candidate, cwd)
			if err == nil {
				return candidate, relative, isGit
			}
		}
		if (isGit && candidate == root) || filepath.Dir(candidate) == candidate {
			break
		}
	}
	return root, rel, isGit
}
