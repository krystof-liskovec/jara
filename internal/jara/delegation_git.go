package jara

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
)

func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

// prepareTaskWorktree gives a delegated task its own branch and checkout
// based on the caller's HEAD, preserving the caller's subdirectory.
// Uncommitted caller changes are not copied, which the tool description states.
func prepareTaskWorktree(task *delegatedTask) error {
	repository, err := gitIn(task.OriginCWD, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("worktree isolation needs a Git repository; use isolation \"shared\": %w", err)
	}
	rel, err := filepath.Rel(repository, task.OriginCWD)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = "."
	}
	task.Repository = repository
	task.Branch = "jara/" + task.ID
	task.Worktree = filepath.Join(fsutil.DataRoot(), "worktrees", task.ID)
	if _, err = gitIn(repository, "worktree", "add", "-b", task.Branch, task.Worktree, "HEAD"); err != nil {
		return err
	}
	task.CWD = filepath.Join(task.Worktree, rel)
	return nil
}

// gitIdentityArgs supplies a fallback author so task commits work on hosts
// without a configured Git identity; a configured identity is kept.
func gitIdentityArgs(dir string) []string {
	if email, err := gitIn(dir, "config", "user.email"); err == nil && email != "" {
		return nil
	}
	return []string{"-c", "user.name=Jara task", "-c", "user.email=jara@localhost"}
}

// mergeTask commits the task's worktree changes on its branch and merges the
// branch into the caller's checkout. Conflicts are left in place for the
// caller to resolve, matching an ordinary git merge.
func mergeTask(task delegatedTask, cleanup bool) (string, error) {
	if task.Worktree == "" {
		return "", fmt.Errorf("task %s ran in the shared checkout; its changes are already there", task.ID)
	}
	if task.Status == taskRunning {
		return "", fmt.Errorf("task %s is still running", task.ID)
	}
	if task.Merged {
		return "", fmt.Errorf("task %s was already merged", task.ID)
	}
	var report strings.Builder
	status, err := gitIn(task.Worktree, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		if _, err = gitIn(task.Worktree, "add", "-A"); err != nil {
			return "", err
		}
		message := fmt.Sprintf("%s task %s: %s", protocol.AgentDisplayName(task.Agent), task.ID, textutil.FitText(task.Title, 60))
		args := append(gitIdentityArgs(task.Worktree), "commit", "--no-verify", "-m", message)
		if _, err = gitIn(task.Worktree, args...); err != nil {
			return "", err
		}
		report.WriteString("Committed the task's uncommitted changes on " + task.Branch + ".\n")
	}
	ahead, err := gitIn(task.Repository, "rev-list", "--count", "HEAD.."+task.Branch)
	if err != nil {
		return "", err
	}
	if ahead == "0" {
		report.WriteString("The task branch has no new commits; nothing to merge.\n")
	} else {
		stat, _ := gitIn(task.Repository, "diff", "--stat", "HEAD..."+task.Branch)
		args := append(gitIdentityArgs(task.Repository), "merge", "--no-ff", "--no-edit", task.Branch)
		out, mergeErr := gitIn(task.OriginCWD, args...)
		if mergeErr != nil {
			return report.String() + out, fmt.Errorf("merge of %s stopped; resolve the conflicts in %s and commit, or run git merge --abort: %s", task.Branch, task.Repository, out)
		}
		report.WriteString("Merged " + task.Branch + " into the current branch:\n" + stat + "\n")
	}
	if cleanup {
		if _, err = gitIn(task.Repository, "worktree", "remove", "--force", task.Worktree); err == nil {
			_, _ = gitIn(task.Repository, "branch", "-D", task.Branch)
			report.WriteString("Removed the task worktree and branch.\n")
		}
	}
	return strings.TrimSpace(report.String()), nil
}

// taskDiff summarizes what a task changed relative to its starting point.
func taskDiff(task delegatedTask, limit int) string {
	if task.Worktree == "" {
		return ""
	}
	stat, err := gitIn(task.Worktree, "diff", "HEAD", "--stat")
	if err != nil {
		return ""
	}
	base, _ := gitIn(task.Repository, "merge-base", "HEAD", task.Branch)
	committed := ""
	if base != "" {
		committed, _ = gitIn(task.Worktree, "diff", "--stat", base+"..HEAD")
	}
	return textutil.TailText(strings.TrimSpace(committed+"\n"+stat), limit)
}
