package jara

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var commitIDRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// gitCheckoutState identifies the commit a sync endpoint has checked out. An
// empty Head means the endpoint is not a Git checkout and syncs files only.
type gitCheckoutState struct {
	Head   string `json:"head,omitempty"`
	Branch string `json:"branch,omitempty"`
}

func (s gitCheckoutState) describe() string {
	if s.Branch == "" {
		return "a detached HEAD at " + shortCommit(s.Head)
	}
	return "branch " + strconv.Quote(s.Branch)
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func checkoutState(root string) gitCheckoutState {
	top, err := gitOutput(root, "rev-parse", "--show-toplevel")
	if err != nil || !sameDirectory(top, root) {
		return gitCheckoutState{}
	}
	head := gitHead(root)
	if !commitIDRE.MatchString(head) {
		return gitCheckoutState{}
	}
	branch, _ := gitOutput(root, "symbolic-ref", "--short", "-q", "HEAD")
	return gitCheckoutState{Head: head, Branch: branch}
}

func sameDirectory(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func hasCommit(root, commit string) bool {
	return exec.Command("git", "-C", root, "cat-file", "-e", commit+"^{commit}").Run() == nil
}

func isAncestor(root, ancestor, descendant string) bool {
	return exec.Command("git", "-C", root, "merge-base", "--is-ancestor", ancestor, descendant).Run() == nil
}

// gitRun runs a Git command quietly and includes its output in any error.
func gitRun(root string, args ...string) error {
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitNullList(root string, args ...string) ([]string, error) {
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return nil, gitError(gitSubcommand(args), err)
	}
	var names []string
	for _, name := range bytes.Split(output, []byte{0}) {
		if len(name) > 0 {
			names = append(names, string(name))
		}
	}
	return names, nil
}

func commitPathsChanged(root, from, to string) ([]string, error) {
	return gitNullList(root, "diff", "--name-only", "-z", "--no-renames", from, to, "--")
}

// fastForwardCheckout moves root from expected to target only when that is a
// fast-forward. Uncommitted files that already match target count as
// converged; other uncommitted changes to files the commits touch stop it
// before anything changes.
// fastForwardCheckout moves the checkout from expected to target. synced
// holds what the last sync recorded for paths (see agreedSyncRecord): an
// uncommitted file still holding it was only mirrored from the other side and
// may be replaced.
func fastForwardCheckout(root string, expected gitCheckoutState, target, bundle string, synced map[string]string) error {
	if current := checkoutState(root); current != expected {
		return fmt.Errorf("checkout changed during sync (now %s at %s); retry", current.describe(), shortCommit(current.Head))
	}
	if bundle != "" {
		if err := gitRun(root, "fetch", "--quiet", bundle, "HEAD"); err != nil {
			return err
		}
	}
	if !hasCommit(root, target) {
		return fmt.Errorf("commit %s was not transferred; retry", shortCommit(target))
	}
	if !isAncestor(root, expected.Head, target) {
		return fmt.Errorf("commit %s does not descend from %s; histories have diverged", shortCommit(target), shortCommit(expected.Head))
	}
	if err := clearConvergedChanges(root, expected.Head, target, synced); err != nil {
		return err
	}
	return gitRun(root, "merge", "--ff-only", "--quiet", target)
}

// clearConvergedChanges resolves uncommitted paths that already hold the
// incoming content so Git can fast-forward over them: content of the target,
// or of any incoming commit (an earlier file-based sync mirrored work that was
// committed later, then changed again). Either way the incoming history keeps
// it. So are paths unchanged since the last sync recorded them in synced: the
// other side changed them further, then committed. It changes nothing when
// any touched path holds other content.
func clearConvergedChanges(root, from, to string, synced map[string]string) error {
	changed, err := commitPathsChanged(root, from, to)
	if err != nil {
		return err
	}
	touched := map[string]bool{}
	for _, name := range changed {
		touched[name] = true
	}
	status, err := gitNullList(root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return err
	}
	var converged, blocked []string
	for _, record := range status {
		if len(record) < 4 || !touched[record[3:]] {
			continue
		}
		name := record[3:]
		want, err := gitTreeEntry(root, to, name)
		if err != nil {
			return err
		}
		have, err := worktreeEntry(root, name)
		if err != nil {
			return err
		}
		if want != have {
			if want, err = incomingContentMatches(root, from, to, name, have); err != nil {
				return err
			}
		}
		if want == have || unchangedSinceSync(root, name, synced) {
			converged = append(converged, name)
		} else {
			blocked = append(blocked, name)
		}
	}
	if len(blocked) > 0 {
		sort.Strings(blocked)
		return fmt.Errorf("uncommitted changes would be overwritten by incoming commits: %s; commit, stash or revert them, then retry", strings.Join(blocked, ", "))
	}
	for _, name := range converged {
		original, err := gitTreeEntry(root, from, name)
		if err != nil {
			return err
		}
		if original != "" {
			err = gitRun(root, "--literal-pathspecs", "checkout", from, "--", name)
		} else {
			err = gitRun(root, "--literal-pathspecs", "rm", "--cached", "--quiet", "--ignore-unmatch", "--", name)
			if err == nil {
				err = os.Remove(filepath.Join(root, filepath.FromSlash(name)))
				if os.IsNotExist(err) {
					err = nil
				}
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// incomingContentMatches returns have when a commit in from..to holds that
// content of name, and something else otherwise.
func incomingContentMatches(root, from, to, name, have string) (string, error) {
	// Hashes only, one per line: Git before 2.47 ignores rev-list -z.
	list, err := exec.Command("git", "-C", root, "--literal-pathspecs", "rev-list", from+".."+to, "--", name).Output()
	if err != nil {
		return "", gitError("rev-list", err)
	}
	for _, commit := range strings.Fields(string(list)) {
		entry, err := gitTreeEntry(root, commit, name)
		if err != nil {
			return "", err
		}
		if entry == have {
			return have, nil
		}
	}
	return "\x00differs", nil
}

// gitTreeEntry returns "mode blob" for a file in commit, or "" when absent.
func gitTreeEntry(root, commit, name string) (string, error) {
	entries, err := gitNullList(root, "--literal-pathspecs", "ls-tree", "-z", commit, "--", name)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		header, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(header)
		if ok && path == name && len(fields) == 3 {
			return fields[0] + " " + fields[2], nil
		}
	}
	return "", nil
}

// worktreeEntry returns "mode blob" for a working-tree file, matching
// gitTreeEntry, or "" when absent.
func worktreeEntry(root, name string) (string, error) {
	full := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		cmd := exec.Command("git", "-C", root, "hash-object", "--stdin")
		cmd.Stdin = strings.NewReader(target)
		blob, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", name, err)
		}
		return "120000 " + strings.TrimSpace(string(blob)), nil
	case info.Mode().IsRegular():
		blob, err := gitOutput(root, "hash-object", "--", name)
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", name, err)
		}
		mode := "100644"
		if info.Mode()&0111 != 0 {
			mode = "100755"
		}
		return mode + " " + blob, nil
	default:
		return "unsupported", nil
	}
}

// gitSubcommand names the command in args, after any global options.
func gitSubcommand(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return "command"
}

// gitError reports a failed Git command with what Git printed.
func gitError(command string, err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		return fmt.Errorf("git %s: %w: %s", command, err, strings.TrimSpace(string(exit.Stderr)))
	}
	return fmt.Errorf("git %s: %w", command, err)
}
