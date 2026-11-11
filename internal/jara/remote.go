package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

const workspaceLockOwnerFile = "owner.pid"
const workspaceLockStaleGrace = time.Minute

type Workspace struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Path       string `json:"path"`
	URL        string `json:"url,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Created    string `json:"created"`
	Ready      bool   `json:"ready"`
	SourceHost string `json:"source_host,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
}

type Diagnosis struct {
	Protocol int
	Version  string
	Root     string
	OK       bool
	Checks   []string
}

type Session struct {
	Conversation string
	Name         string
	Workspace    string
	Attached     int
	Background   bool
	ChatDaemon   bool
	Dead         bool
	CreatedAt    int64
	LastAttach   int64
	Label        string
	AgentState   string
	Agent        string
}

func workspacePath(id string) (string, error) {
	if !nameRE.MatchString(id) {
		return "", fmt.Errorf("invalid workspace ID")
	}
	return filepath.Join(fsutil.DataRoot(), "workspaces", id), nil
}

func workspaceInfo(id string) (Workspace, error) {
	var w Workspace
	p, err := workspacePath(id)
	if err != nil {
		return w, err
	}
	b, err := os.ReadFile(p + ".json")
	if err != nil {
		return w, err
	}
	err = json.Unmarshal(b, &w)
	if err == nil {
		w.Branch, _ = gitOutput(w.Path, "symbolic-ref", "--short", "-q", "HEAD")
	}
	return w, err
}

func lockWorkspace(id string) (func(), error) {
	gate, err := lockLifecycle()
	if err != nil {
		return nil, err
	}
	defer gate()
	if err = admitWorkspace(); err != nil {
		return nil, err
	}
	p, err := workspacePath(id)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	lock := p + ".lock"
	if err = os.Mkdir(lock, 0700); err != nil {
		if !os.IsExist(err) {
			return nil, err
		}
		recovered, recoverErr := recoverWorkspaceLock(lock)
		if recoverErr != nil {
			return nil, recoverErr
		}
		if !recovered {
			return nil, fmt.Errorf("workspace operation is active or its lock is too recent to recover: %s", lock)
		}
		if err = os.Mkdir(lock, 0700); err != nil {
			return nil, fmt.Errorf("workspace operation already in progress: %w", err)
		}
	}
	owner := filepath.Join(lock, workspaceLockOwnerFile)
	if err = os.WriteFile(owner, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		_ = os.Remove(owner)
		_ = os.Remove(lock)
		return nil, fmt.Errorf("record workspace lock owner: %w", err)
	}
	return func() {
		_ = os.Remove(owner)
		_ = os.Remove(lock)
	}, nil
}

// recoverWorkspaceLock runs while holding the lifecycle lock. PID markers
// protect live operations; old ownerless locks expire after a short grace.
func recoverWorkspaceLock(lock string) (bool, error) {
	info, err := os.Lstat(lock)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("workspace lock is not a valid directory: %s", lock)
	}
	markers := []string{workspaceLockOwnerFile, "chat-daemon.pid"}
	foundMarker := false
	for _, name := range markers {
		owner := filepath.Join(lock, name)
		markerInfo, markerErr := os.Lstat(owner)
		if os.IsNotExist(markerErr) {
			continue
		}
		if markerErr != nil {
			return false, markerErr
		}
		if !markerInfo.Mode().IsRegular() {
			return false, fmt.Errorf("workspace lock owner marker is not a regular file: %s", owner)
		}
		foundMarker = true
		data, readErr := os.ReadFile(owner)
		if readErr != nil {
			return false, readErr
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr != nil || pid < 2 {
			return false, fmt.Errorf("workspace lock owner is invalid; inspect %s", owner)
		}
		if chatProcessExists(pid) {
			return false, nil
		}
		if err = os.Remove(owner); err != nil {
			return false, err
		}
	}
	if !foundMarker && time.Since(info.ModTime()) < workspaceLockStaleGrace {
		return false, nil
	}
	if err = os.Remove(lock); err != nil {
		return false, fmt.Errorf("remove stale workspace lock: %w", err)
	}
	return true, nil
}

func workspaceOperationLocked(id string) (bool, error) {
	p, err := workspacePath(id)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(p + ".lock")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("workspace lock is not a valid directory")
	}
	return true, nil
}
