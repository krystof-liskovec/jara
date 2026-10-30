package jara

import (
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

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func tmux(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", "jara"}, args...)...)
}

func sessions() ([]Session, error) {
	// has-session distinguishes an absent server from malformed output.
	probe := tmux("list-sessions", "-F", "#{session_name}|#{@jara_workspace}|#{session_attached}")
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
		return fmt.Errorf("use jara connect [options] [WORKSPACE]")
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
	name := "jara-" + q.ID + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
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
	if err = runTmux("set-option", "-t", name, "@jara_workspace", q.ID); err != nil {
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
