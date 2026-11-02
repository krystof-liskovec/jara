package jara

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func remoteWorkspaces() ([]Workspace, error) {
	directory := filepath.Join(dataRoot(), "workspaces")
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return []Workspace{}, nil
		}
		return nil, err
	}
	workspaces := make([]Workspace, 0, len(entries))
	seen := make(map[string]bool)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !nameRE.MatchString(id) {
			continue
		}
		seen[id] = true
		workspace, err := workspaceInfo(id)
		if err != nil {
			return nil, fmt.Errorf("read workspace %s: %w", id, err)
		}
		workspaces = append(workspaces, workspace)
	}
	for _, entry := range entries {
		id := entry.Name()
		if !nameRE.MatchString(id) || seen[id] || !entry.IsDir() {
			continue
		}
		workspaces = append(workspaces, Workspace{ID: id, Name: "(incomplete)", Path: filepath.Join(directory, id)})
	}
	sort.Slice(workspaces, func(i, j int) bool {
		left, right := strings.ToLower(workspaces[i].Name), strings.ToLower(workspaces[j].Name)
		if left != right {
			return left < right
		}
		return workspaces[i].ID < workspaces[j].ID
	})
	return workspaces, nil
}

func remoteDeleteWorkspace(id string) error {
	unlock, err := lockWorkspace(id)
	if err != nil {
		return err
	}
	defer unlock()
	expected, err := workspacePath(id)
	if err != nil {
		return err
	}
	workspace, err := workspaceInfo(id)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if _, statErr := os.Lstat(expected); statErr != nil {
			return err
		}
		workspace = Workspace{ID: id, Name: "(incomplete)", Path: expected}
	}
	if workspace.ID != id {
		return fmt.Errorf("workspace metadata ID does not match its directory; refusing to delete it")
	}
	if filepath.Clean(workspace.Path) != filepath.Clean(expected) {
		return fmt.Errorf("workspace metadata path does not match its ID; refusing to delete it")
	}
	if info, statErr := os.Lstat(expected); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace path is a symlink; refusing to delete it")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	sessions, err := sessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.Workspace == id {
			return fmt.Errorf("workspace has a running session %s; stop it before deleting", menuText(session.Name))
		}
	}
	if err = removeManagedWorkspaceWorktrees(workspace); err != nil {
		return err
	}
	if err = os.RemoveAll(expected); err != nil {
		return fmt.Errorf("remove workspace files: %w", err)
	}
	if err = os.Remove(expected + ".json"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove workspace metadata: %w", err)
	}
	return nil
}

func removeManagedWorkspaceWorktrees(workspace Workspace) error {
	rootInfo, err := os.Stat(workspace.Path)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if !rootInfo.IsDir() {
		return nil
	}
	gitInfo, err := os.Lstat(filepath.Join(workspace.Path, ".git"))
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if gitInfo.Mode()&os.ModeSymlink != 0 || !gitInfo.IsDir() {
		return nil
	}
	cmd := exec.Command("git", "-C", workspace.Path, "worktree", "list", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("list linked Git worktrees: %w", err)
	}
	root, err := filepath.Abs(workspace.Path)
	if err != nil {
		return err
	}
	managedRoot, err := filepath.Abs(filepath.Join(dataRoot(), "worktrees"))
	if err != nil {
		return err
	}
	var managedWorktrees []string
	for _, line := range strings.Split(string(output), "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok || path == "" {
			continue
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return err
		}
		if filepath.Clean(path) == filepath.Clean(root) {
			continue
		}
		if !pathWithin(managedRoot, path) {
			if _, statErr := os.Lstat(path); statErr == nil {
				return fmt.Errorf("workspace has a linked Git worktree outside Jara's worktree directory: %s; remove it first", path)
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			continue
		}
		info, statErr := os.Lstat(path)
		if os.IsNotExist(statErr) {
			continue
		} else if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("linked Git worktree path is a symlink; refusing to delete it")
		}
		resolvedPath, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return resolveErr
		}
		resolvedRoot, resolveErr := filepath.EvalSymlinks(managedRoot)
		if resolveErr != nil {
			return resolveErr
		}
		if !pathWithin(resolvedRoot, resolvedPath) {
			return fmt.Errorf("linked Git worktree resolves outside Jara's worktree directory; refusing to delete it")
		}
		managedWorktrees = append(managedWorktrees, path)
	}
	for _, path := range managedWorktrees {
		remove := exec.Command("git", "-C", workspace.Path, "worktree", "remove", "--force", path)
		if output, err = remove.CombinedOutput(); err != nil {
			return fmt.Errorf("remove linked worktree %s: %w: %s", path, err, strings.TrimSpace(string(output)))
		}
	}
	prune := exec.Command("git", "-C", workspace.Path, "worktree", "prune")
	if output, err = prune.CombinedOutput(); err != nil {
		return fmt.Errorf("prune Git worktree records: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)
}
