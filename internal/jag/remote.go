package jag

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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
	Name      string
	Workspace string
	Attached  int
}

func dataRoot() string {
	if p := os.Getenv("JAG_DATA"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "jag")
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
func remoteRPC(q Request) error {
	switch q.Op {
	case "editor-snapshot", "editor-apply":
		gate, err := lockLifecycle()
		if err != nil {
			return err
		}
		defer gate()
		if err = admitWorkspace(); err != nil {
			return err
		}
		root, err := editorConfigPath(q.ID)
		if err != nil {
			return err
		}
		if _, err = exec.LookPath(q.ID); err != nil {
			return fmt.Errorf("%s is not installed; select it in the host environment and rebuild", q.ID)
		}
		if err = os.MkdirAll(root, 0700); err != nil {
			return err
		}
		if q.Op == "editor-snapshot" {
			files, err := snapshot(root)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(files)
		}
		var u Update
		if err = json.NewDecoder(io.LimitReader(os.Stdin, maxTransfer*2)).Decode(&u); err != nil {
			return err
		}
		return applyUpdate(root, u)
	case "doctor":
		d := Diagnosis{Protocol: Protocol, Version: Version, Root: dataRoot(), OK: true}
		for _, tool := range []string{"git", "tmux", "codex", "ssh", "mise"} {
			if _, err := exec.LookPath(tool); err != nil {
				d.OK = false
				d.Checks = append(d.Checks, "MISSING "+tool+": rebuild the environment with this tool")
			} else {
				d.Checks = append(d.Checks, "OK "+tool)
			}
		}
		if err := os.MkdirAll(dataRoot(), 0700); err != nil {
			d.OK = false
			d.Checks = append(d.Checks, "storage: "+err.Error())
		} else {
			f, e := os.CreateTemp(dataRoot(), ".doctor-")
			if e != nil {
				d.OK = false
				d.Checks = append(d.Checks, "storage not writable: "+e.Error())
			} else {
				f.Close()
				os.Remove(f.Name())
				d.Checks = append(d.Checks, "OK writable storage")
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		auth := exec.CommandContext(ctx, "codex", "login", "status")
		if err := auth.Run(); err != nil {
			d.Checks = append(d.Checks, "SETUP Codex authentication: ssh to the environment and run codex login --device-auth")
		}
		probe, err := os.MkdirTemp(dataRoot(), ".sandbox-check-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(probe)
		sandboxCtx, sandboxCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sandboxCancel()
		sandbox := exec.CommandContext(sandboxCtx, "codex", "sandbox", "-c", "sandbox_mode=\"workspace-write\"", "--", "/bin/sh", "-c", "printf jag-sandbox-ok > check.txt")
		sandbox.Dir = probe
		if err = sandbox.Run(); err != nil {
			d.OK = false
			d.Checks = append(d.Checks, "SETUP Codex sandbox cannot execute: deploy with Jag's Compose seccomp profile and enable unprivileged user namespaces on the host (see docs/verification.md)")
		} else {
			d.Checks = append(d.Checks, "OK real Codex sandbox execution")
		}
		if os.Getenv("JAG_DOCKER") == "1" {
			dockerCtx, dockerCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer dockerCancel()
			info, e := exec.CommandContext(dockerCtx, "docker", "info", "--format", "{{json .SecurityOptions}}").Output()
			if e != nil || !strings.Contains(string(info), "name=rootless") {
				d.OK = false
				d.Checks = append(d.Checks, "SETUP rootless Docker unavailable: check the docker sidecar and shared socket")
			} else {
				d.Checks = append(d.Checks, "OK rootless Docker daemon")
			}
		}
		return json.NewEncoder(os.Stdout).Encode(d)
	case "info":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(w)
	case "create":
		unlock, err := lockWorkspace(q.ID)
		if err != nil {
			return err
		}
		defer unlock()
		p, _ := workspacePath(q.ID)
		if _, err = os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			if existing, e := workspaceInfo(q.ID); e == nil && !existing.Ready && existing.URL == q.URL {
				fmt.Fprintln(os.Stderr, "Retrying unfinished workspace initialization.")
				return nil
			}
			return fmt.Errorf("destination already exists; no files changed: %s", p)
		}
		if q.URL != "" {
			if err = validateOrigin(q.URL); err != nil {
				return err
			}
			cmd := exec.Command("git", "clone", "--no-checkout", "--", q.URL, p)
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if err = cmd.Run(); err != nil {
				return fmt.Errorf("clone failed; configure remote Git authentication: %w", err)
			}
		} else if err = os.MkdirAll(p, 0700); err != nil {
			return err
		}
		return writeJSON(p+".json", Workspace{ID: q.ID, Name: q.Name, Path: p, URL: q.URL, Branch: q.Branch, Created: time.Now().UTC().Format(time.RFC3339)})
	case "checkout":
		unlock, err := lockWorkspace(q.ID)
		if err != nil {
			return err
		}
		defer unlock()
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		temp, err := os.CreateTemp(dataRoot(), "bundle-*")
		if err != nil {
			return err
		}
		defer os.Remove(temp.Name())
		if _, err = io.Copy(temp, os.Stdin); err != nil {
			temp.Close()
			return err
		}
		if err = temp.Close(); err != nil {
			return err
		}
		if err = runGit(w.Path, "fetch", temp.Name(), "HEAD"); err != nil {
			return err
		}
		if q.Commit == "" || strings.HasPrefix(q.Commit, "-") {
			return fmt.Errorf("invalid commit")
		}
		if q.Branch == "" {
			err = runGit(w.Path, "checkout", "--detach", q.Commit)
		} else {
			if err = runGit(w.Path, "check-ref-format", "--branch", q.Branch); err != nil {
				return err
			}
			err = runGit(w.Path, "checkout", "-B", q.Branch, q.Commit)
		}
		if err != nil {
			return err
		}
		w.Ready = true
		return writeJSON(w.Path+".json", w)
	case "snapshot":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		files, err := snapshotWithIgnored(w.Path, q.IncludeIgnored)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(files)
	case "exists":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		var paths []string
		if err = json.NewDecoder(io.LimitReader(os.Stdin, maxTransfer)).Decode(&paths); err != nil {
			return err
		}
		present, err := existingPaths(w.Path, paths)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(present)
	case "guard":
		if _, err := workspaceInfo(q.ID); err != nil {
			return err
		}
		ss, err := sessions()
		if err != nil {
			return err
		}
		if !q.Force {
			for _, s := range ss {
				if s.Workspace == q.ID {
					return fmt.Errorf("running agent in workspace; stop it or explicitly use --force")
				}
			}
		}
		return nil
	case "apply":
		unlock, err := lockWorkspace(q.ID)
		if err != nil {
			return err
		}
		defer unlock()
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		ss, err := sessions()
		if err != nil {
			return err
		}
		if !q.Force {
			for _, s := range ss {
				if s.Workspace == q.ID {
					return fmt.Errorf("running agent in workspace; detach is insufficient, stop it or explicitly use --force")
				}
			}
		}
		var update Update
		if err = json.NewDecoder(io.LimitReader(os.Stdin, maxTransfer*2)).Decode(&update); err != nil {
			return err
		}
		if err = applyUpdate(w.Path, update); err != nil {
			return err
		}
		if !w.Ready {
			w.Ready = true
			return writeJSON(w.Path+".json", w)
		}
		return nil
	default:
		return fmt.Errorf("unknown RPC operation")
	}
}
func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func tmux(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", "jag"}, args...)...)
}
func sessions() ([]Session, error) {
	// has-session distinguishes an absent server from malformed output.
	probe := tmux("list-sessions", "-F", "#{session_name}|#{@jag_workspace}|#{session_attached}")
	b, err := probe.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (strings.Contains(string(ee.Stderr), "no server running") || strings.Contains(string(ee.Stderr), "No such file")) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var ss []Session
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		p := strings.Split(line, "|")
		if len(p) != 3 || p[1] == "" {
			continue
		}
		n, _ := strconv.Atoi(p[2])
		ss = append(ss, Session{p[0], p[1], n})
	}
	return ss, nil
}

// connectCommand runs inside the environment, without client configuration or SSH.
func connectCommand(args []string) error {
	f := flag.NewFlagSet("connect", flag.ContinueOnError)
	resume := f.Bool("resume", false, "use plain codex resume when starting a terminal")
	fresh := f.Bool("new", false, "start another Codex terminal")
	worktree := f.Bool("worktree", false, "start a new terminal in a Git worktree")
	takeover := f.Bool("takeover", false, "detach other clients when attaching")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() > 1 {
		return fmt.Errorf("use jag connect [options] [WORKSPACE]")
	}
	id := ""
	if f.NArg() == 1 {
		id = f.Arg(0)
		if !nameRE.MatchString(id) {
			return fmt.Errorf("invalid workspace ID")
		}
	}
	return remoteOpen(Request{ID: id, Resume: *resume, New: *fresh || *worktree, Worktree: *worktree, Takeover: *takeover})
}

func remoteOpen(q Request) error {
	if q.ID == "" {
		ss, err := sessions()
		if err != nil {
			return err
		}
		files, err := filepath.Glob(filepath.Join(dataRoot(), "workspaces", "*.json"))
		if err != nil {
			return err
		}
		sort.Strings(files)
		var workspaces []Workspace
		byID := map[string]Workspace{}
		for _, f := range files {
			b, e := os.ReadFile(f)
			if e != nil {
				return e
			}
			var w Workspace
			if e = json.Unmarshal(b, &w); e != nil {
				return e
			}
			workspaces = append(workspaces, w)
			byID[w.ID] = w
		}
		var items []menuItem
		numbers := map[string]int{}
		for _, s := range ss {
			w, ok := byID[s.Workspace]
			if !ok {
				w = Workspace{ID: s.Workspace, Name: s.Workspace}
			}
			numbers[s.Workspace]++
			items = append(items, sessionItem(w, s, numbers[s.Workspace]))
		}
		var ws []Workspace
		var modes []bool
		for _, w := range workspaces {
			status := "Open"
			if !w.Ready {
				status = "Setup unfinished"
			}
			ws = append(ws, w)
			modes = append(modes, false)
			items = append(items, menuItem{Title: workspaceTitle(w), Detail: status + " · " + workspaceBadge(w), Workspace: w.ID})
			ws = append(ws, w)
			modes = append(modes, true)
			items = append(items, menuItem{Title: "New: " + workspaceTitle(w), Detail: "New session · " + workspaceBadge(w), Workspace: w.ID})
		}
		items = append(items, menuItem{Title: "Setup diagnostics", Detail: "Check tools, storage and authentication"})
		n, err := selectMenu("Sessions & workspaces", items)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if n < len(ss) {
			return attach(ss[n], q.Takeover)
		}
		if n == len(items)-1 {
			return remoteRPC(Request{Op: "doctor"})
		}
		q.ID = ws[n-len(ss)].ID
		q.New = q.New || modes[n-len(ss)]
	}

	unlock, err := lockWorkspace(q.ID)
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	w, err := workspaceInfo(q.ID)
	if err != nil {
		return err
	}
	if !w.Ready {
		return fmt.Errorf("workspace initialization is unfinished; retry the original local --clone/--copy command")
	}
	ss, err := sessions()
	if err != nil {
		return err
	}
	var matches []Session
	for _, s := range ss {
		if s.Workspace == q.ID {
			matches = append(matches, s)
		}
	}
	if !q.New && len(matches) > 0 {
		unlock()
		locked = false
		i := 0
		if len(matches) > 1 {
			items := make([]menuItem, len(matches))
			for j, s := range matches {
				items[j] = sessionItem(w, s, j+1)
			}
			i, err = selectMenu("Running sessions", items)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		return attach(matches[i], q.Takeover)
	}
	path := w.Path
	name := "jag-" + q.ID + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if q.Worktree {
		path = filepath.Join(dataRoot(), "worktrees", name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err = runGit(w.Path, "worktree", "add", "-b", name, path, "HEAD"); err != nil {
			return fmt.Errorf("worktree requires a Git repository: %w", err)
		}
	} else if q.New && len(matches) > 0 {
		yes, e := confirm("Another agent uses these files. Start another in the same workspace?")
		if e != nil {
			return e
		}
		if !yes {
			return nil
		}
	}
	if q.Subdir != "" && q.Subdir != "." {
		path, err = safePath(path, q.Subdir)
		if err != nil {
			return err
		}
	}
	if info, e := os.Stat(path); e != nil || !info.IsDir() {
		return fmt.Errorf("remote working directory is missing: %s", path)
	}
	if err = prepareTools(path); err != nil {
		return err
	}
	args := []string{"new-session", "-d", "-s", name, "-c", path, "mise", "exec", "--", "/usr/local/bin/codex"}
	if q.Resume {
		args = append(args, "resume")
	}
	if err = runTmux(args...); err != nil {
		return fmt.Errorf("start terminal: %w", err)
	}
	if err = runTmux("set-option", "-t", name, "@jag_workspace", q.ID); err != nil {
		return err
	}
	unlock()
	locked = false
	return attach(Session{Name: name, Workspace: q.ID}, q.Takeover)
}

func runTmux(args ...string) error { cmd := tmux(args...); cmd.Stderr = os.Stderr; return cmd.Run() }
func attach(s Session, takeover bool) error {
	if s.Attached > 0 {
		fmt.Fprintf(os.Stderr, "Sharing this terminal with %d attached client(s).\n", s.Attached)
	}
	args := []string{"attach-session", "-t", s.Name}
	if takeover {
		args = append(args, "-d")
	}
	fmt.Fprintln(os.Stderr, "Detach with Ctrl-b d; Codex keeps running.")
	return interactive(tmux(args...))
}
