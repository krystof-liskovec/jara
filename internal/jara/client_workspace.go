package jara

import (
	"bufio"
	"fmt"
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
	fmt.Fprint(os.Stderr, prompt+" [y/N] ")
	s, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes", nil
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
