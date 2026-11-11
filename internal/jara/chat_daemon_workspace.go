package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

func recoverChatDaemonWorkspaceMetadata(id string, paths chatDaemonPaths) error {
	expected, err := workspacePath(id)
	if err != nil {
		return err
	}
	metadata := expected + ".json"
	if _, err := os.Lstat(metadata); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	data, err := os.ReadFile(paths.state)
	if err != nil {
		return fmt.Errorf("persistent Jara session state is unavailable for workspace recovery: %w", err)
	}
	var state chatDaemonState
	if err = json.Unmarshal(data, &state); err != nil || state.WorkspaceID != id || state.CWD == "" {
		return fmt.Errorf("persistent Jara session state cannot recover workspace metadata")
	}
	rootInfo, err := os.Lstat(expected)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace directory is not a regular directory")
	}
	cwdInfo, err := os.Stat(state.CWD)
	if err != nil {
		return fmt.Errorf("saved session directory is unavailable: %w", err)
	}
	if !cwdInfo.IsDir() {
		return fmt.Errorf("saved session directory is not a directory")
	}
	root, err := filepath.Abs(expected)
	if err != nil {
		return err
	}
	cwd, err := filepath.Abs(state.CWD)
	if err != nil {
		return err
	}
	worktrees, err := filepath.Abs(filepath.Join(fsutil.DataRoot(), "worktrees"))
	if err != nil {
		return err
	}
	if !chatDaemonPathWithin(root, cwd) && !chatDaemonPathWithin(worktrees, cwd) {
		return fmt.Errorf("saved session directory is outside its workspace")
	}
	if chatDaemonPathWithin(worktrees, cwd) {
		gitRoot, gitErr := gitOutput(root, "rev-parse", "--show-toplevel")
		if gitErr != nil || filepath.Clean(gitRoot) != filepath.Clean(root) {
			return fmt.Errorf("workspace is not a valid Git root for its saved worktree")
		}
	}

	name := strings.TrimSpace(state.Workspace)
	if name == "" {
		name = filepath.Base(root)
	}
	created := time.Now().UTC().Format(time.RFC3339)
	if state.CreatedAt > 0 {
		created = time.Unix(0, state.CreatedAt).UTC().Format(time.RFC3339)
	}
	return fsutil.WriteJSON(metadata, Workspace{ID: id, Name: name, Path: expected, Created: created, Ready: true})
}

func chatDaemonPathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
