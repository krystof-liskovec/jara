package jara

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func syncLocalChangesBeforeSession(r Remote, b Binding, root, remoteName string, includeIgnored, isGit bool, reader *bufio.Reader, persistentJara ...bool) error {
	summary, err := localPushChangesDetails(b, root, includeIgnored, isGit)
	if err != nil {
		return fmt.Errorf("local changes could not be checked: %w; run `jara sync push` if needed", err)
	}
	if !summary.Pending {
		return nil
	}
	if len(persistentJara) > 0 && persistentJara[0] {
		var status struct {
			Active bool `json:"active"`
			Idle   bool `json:"idle"`
		}
		// An idle, detached session stops for the sync and resumes its conversation.
		if stateErr := rpc(r, Request{Op: "chat-session-state", ID: b.Workspace}, nil, &status); stateErr == nil && status.Active && !status.Idle {
			fmt.Fprintln(os.Stderr, "Reconnecting to the persistent Jara session, which is busy or has a client attached; local files will not sync until it is stopped with /stop.")
			return nil
		}
	}
	// A Git pull may already have brought the remote's changes into this
	// checkout while the saved sync baseline still describes the prior state.
	// Only contact the remote after the local summary found something pending.
	if reconciled, reconcileErr := reconcileLocalPushChangesDetails(r, b, root, includeIgnored, isGit); reconcileErr == nil {
		summary = reconciled
	} else {
		fmt.Fprintf(os.Stderr, "Could not check remote sync state; using the local pending summary: %v\n", reconcileErr)
	}
	if !summary.Pending {
		return nil
	}
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	prompt := localChangesSyncPrompt(summary, remoteName)
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
	if err = syncWorkspace(r, b, root, "push", false, includeIgnored, summary.Deletions); err != nil {
		return fmt.Errorf("local changes could not be synced: %w; retry with `jara sync push`", err)
	}
	return nil
}

func localChangesSyncPrompt(summary localSyncSummary, remoteName string) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Local changes are not synced (about %s of file data). Sync them to %s before starting?", formatSyncSize(summary.Size), remoteName)
	if summary.Deletions {
		prompt.WriteString(" This will also remove files deleted locally from the remote.")
	}
	if len(summary.Files) > 0 {
		prompt.WriteString("\nFiles to upload:")
		for _, name := range summary.Files {
			fmt.Fprintf(&prompt, "\n  + %s", strconv.Quote(name))
		}
	}
	if len(summary.Deleted) > 0 {
		if summary.Deletions {
			prompt.WriteString("\nFiles deleted locally:")
		} else {
			prompt.WriteString("\nFiles deleted locally (remote deletion is not inferred without sync history):")
		}
		for _, name := range summary.Deleted {
			fmt.Fprintf(&prompt, "\n  - %s", strconv.Quote(name))
		}
	}
	return prompt.String()
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
