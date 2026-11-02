package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Workspace struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path"`
	URL     string `json:"url,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Created string `json:"created"`
	Ready   bool   `json:"ready"`
}

type Diagnosis struct {
	Protocol int
	Version  string
	Root     string
	OK       bool
	Checks   []string
}

type Session struct {
	Name       string
	Workspace  string
	Attached   int
	Background bool
	Dead       bool
	CreatedAt  int64
	LastAttach int64
	Label      string
}

func dataRoot() string {
	if p := os.Getenv("JARA_DATA"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "jara")
}

func workspacePath(id string) (string, error) {
	if !nameRE.MatchString(id) {
		return "", fmt.Errorf("invalid workspace ID")
	}
	return filepath.Join(dataRoot(), "workspaces", id), nil
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
		return nil, fmt.Errorf("workspace operation already in progress (or stale lock %s): %w", lock, err)
	}
	return func() { _ = os.Remove(lock) }, nil
}
