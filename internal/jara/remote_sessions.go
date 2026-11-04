package jara

import (
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
	probe := tmux("list-sessions", "-F", "#{session_name}|#{@jara_workspace}|#{session_attached}|#{@jara_background}|#{pane_dead}|#{@jara_created_at}|#{@jara_last_attached}|#{@jara_session_label}|#{@jara_codex_thread}")
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
		if len(p) < 5 {
			continue
		}
		n, _ := strconv.Atoi(p[2])
		session := Session{Name: p[0], Workspace: p[1], Attached: n, Background: p[3] == "1", Dead: p[4] == "1"}
		if len(p) > 5 {
			session.CreatedAt, _ = strconv.ParseInt(p[5], 10, 64)
		}
		if len(p) > 6 {
			session.LastAttach, _ = strconv.ParseInt(p[6], 10, 64)
		}
		if len(p) > 7 {
			session.Label = decodeSessionMenuLabel(p[7])
		}
		if len(p) > 8 {
			session.Conversation = p[8]
		}
		ss = append(ss, session)
	}
	return ss, nil
}

func activeSessions() ([]Session, error) {
	all, err := sessions()
	if err != nil {
		return nil, err
	}
	active := make([]Session, 0, len(all))
	for _, session := range all {
		if session.Background && session.Dead {
			continue
		}
		active = append(active, session)
	}
	return active, nil
}

const defaultMaxSessions = 500

func maxSessions() (int, error) {
	raw := strings.TrimSpace(os.Getenv("JARA_MAX_SESSIONS"))
	if raw == "" {
		return defaultMaxSessions, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > defaultMaxSessions {
		return 0, fmt.Errorf("JARA_MAX_SESSIONS must be an integer from 1 to %d", defaultMaxSessions)
	}
	return limit, nil
}

func checkSessionLimit() error {
	limit, err := maxSessions()
	if err != nil {
		return err
	}
	count, err := tmuxSessionCount()
	if err != nil {
		return err
	}
	if count >= limit {
		return fmt.Errorf("session limit reached (%d); list sessions with `tmux -L jara list-sessions` and close one with `tmux -L jara kill-session -t NAME`", limit)
	}
	return nil
}

func tmuxSessionCount() (int, error) {
	b, err := tmux("list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (strings.Contains(string(exitErr.Stderr), "no server running") || strings.Contains(string(exitErr.Stderr), "No such file")) {
			return 0, nil
		}
		return 0, fmt.Errorf("list sessions: %w", err)
	}
	output := strings.TrimSpace(string(b))
	if output == "" {
		return 0, nil
	}
	return strings.Count(output, "\n") + 1, nil
}

func sessionCountCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("invalid session count invocation")
	}
	count, err := tmuxSessionCount()
	if err != nil {
		fmt.Fprintln(os.Stdout, "Sessions ?")
		return nil
	}
	limit, err := maxSessions()
	if err != nil {
		fmt.Fprintln(os.Stdout, "Sessions ?")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Sessions %d/%d\n", count, limit)
	return nil
}

func sessionUtilityCommand(command string, args []string) error {
	if command == "_session-count" {
		return sessionCountCommand(args)
	}
	if len(args) != 1 {
		return fmt.Errorf("invalid session attachment invocation")
	}
	return recordSessionAttachment(args[0])
}

// connectCommand runs inside the environment, without client configuration or SSH.
func connectCommand(args []string) error {
	args, tail := splitCodexArgs(args)
	f := flag.NewFlagSet("connect", flag.ContinueOnError)
	var codexArgs codexArgList
	f.Var(&codexArgs, "codex-arg", "additional Codex argument (repeatable)")
	chatInterface := f.String("interface", "codex", "conversation interface: codex or jara")
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
	if err := validateChatInterface(*chatInterface); err != nil {
		return err
	}
	return remoteOpen(Request{UserInterface: *chatInterface, ID: id, Resume: *resume, New: *fresh || *worktree, Worktree: *worktree, Takeover: *takeover, CodexArgs: append(codexArgs, tail...)})
}

func remoteOpen(q Request) error {
	if err := validateChatInterface(q.UserInterface); err != nil {
		return err
	}
	if q.Background && strings.TrimSpace(q.Prompt) == "" {
		return fmt.Errorf("background Codex runs require a prompt")
	}
	if len(q.Prompt) > maxBackgroundPrompt {
		return fmt.Errorf("prompt exceeds 64 KiB")
	}
	backgroundPromptHandedOff := false
	defer func() {
		if q.BackgroundPromptID != "" && !backgroundPromptHandedOff {
			if path, err := backgroundPromptPath(q.BackgroundPromptID); err == nil {
				_ = os.Remove(path)
			}
		}
	}()
	codexArgs, err := configuredCodexArgs(q.CodexArgs)
	if err != nil {
		return err
	}
	if q.UserInterface == "jara" {
		if _, err := chatServerArgs(codexArgs); err != nil {
			return err
		}
	}
	if q.ID == "" {
		selected, err := browseProjects()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		if selected.Kind == "tools" {
			return showHostDiagnostics()
		}
		if selected.Kind == "history" {
			if q.Background {
				return fmt.Errorf("Codex session browsing is interactive; use `jara resume`")
			}
			command := codexResumeAllCommand(codexArgs)
			return interactive(exec.Command(command[0], command[1:]...))
		}
		if selected.Kind == "conversation" {
			if q.Background {
				return fmt.Errorf("select a workspace for a background run")
			}
			if selected.Workspace == "" {
				command, e := resumeConversationCommand(selected.Agent, selected.Action, selected.Path, codexArgs)
				if e != nil {
					return e
				}
				if q.UserInterface == "jara" {
					q.Conversation = selected.Action
					q.CodexArgs = codexArgs
					if err := os.Chdir(selected.Path); err != nil {
						return err
					}
					return runJaraChat(q)
				}
				return interactive(exec.Command(command[0], command[1:]...))
			}
			q.Conversation = selected.Action
			q.Agent = selected.Agent
			q.ConversationCWD = selected.Path
			q.ResumeLatest = false
			q.New = true
		}
		if q.Background && (selected.Kind == "terminal" || selected.Kind == "background") && selected.Workspace == "" {
			return fmt.Errorf("this session has no workspace link; choose a workspace to start a background run")
		}
		if (selected.Kind == "terminal" || selected.Kind == "background") && !q.Background {
			warnRunningCodexArgs(q.CodexArgs)
			return attach(Session{Name: selected.Action, Workspace: selected.Workspace}, q.Takeover)
		}

		q.ID = selected.Workspace
		q.New = q.New || selected.Kind == "new"
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
		if s.Workspace == q.ID && !(q.ResumeLatest && s.Background && s.Dead) {
			matches = append(matches, s)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return sessionRecency(matches[i]) > sessionRecency(matches[j])
	})
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
		warnRunningCodexArgs(q.CodexArgs)
		return attach(matches[i], q.Takeover)
	}
	if q.New && !q.Worktree && !q.Background && len(matches) > 0 {
		yes, e := confirm("Another agent uses these files. Start another in the same workspace?")
		if e != nil {
			return e
		}
		if !yes {
			return nil
		}
	}
	if err = checkSessionLimit(); err != nil {
		return err
	}
	path := w.Path
	name := tmuxSessionName(w, q.ID)
	sessionLabel := sessionMenuLabel(q)
	worktreeRoot := ""
	if q.Worktree {
		path = filepath.Join(dataRoot(), "worktrees", name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err = runGit(w.Path, "worktree", "add", "-b", name, path, "HEAD"); err != nil {
			return fmt.Errorf("worktree requires a Git repository: %w", err)
		}
		worktreeRoot = path
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
	if err = prepareToolsQuiet(path); err != nil {
		return err
	}
	background, err := terminalBackground()
	if !q.Background {
		background, err = detectTerminalBackground()
	}
	if err != nil {
		return err
	}
	lifecycleUnlock, err := lockLifecycle()
	if err != nil {
		return err
	}
	lifecycleLocked := true
	defer func() {
		if lifecycleLocked {
			lifecycleUnlock()
		}
	}()
	if err = admitWorkspace(); err != nil {
		return err
	}
	if err = checkSessionLimit(); err != nil {
		if worktreeRoot != "" {
			_ = runGit(w.Path, "worktree", "remove", "--force", worktreeRoot)
			_ = runGit(w.Path, "branch", "-D", name)
		}
		return err
	}
	if q.Background {
		if err = writeBackgroundPrompt(name, q.Prompt); err != nil {
			return err
		}
		q.BackgroundPromptID = name
		q.Prompt = ""
	}
	if err = runTmux("new-session", "-d", "-s", name, "-c", path, "/bin/sleep", "infinity"); err != nil {
		return fmt.Errorf("prepare terminal: %w", err)
	}
	if err = initializeSessionLifecycle(name); err != nil {
		_ = tmux("kill-session", "-t", name).Run()
		return err
	}
	if err = configureTmuxBackground(name, background); err != nil {
		_ = tmux("kill-session", "-t", name).Run()
		return err
	}
	if err = runTmux("set-option", "-t", name, "@jara_workspace", q.ID); err != nil {
		_ = tmux("kill-session", "-t", name).Run()
		return err
	}
	if err = runTmux("set-option", "-t", name, "@jara_session_label", encodeSessionMenuLabel(sessionLabel)); err != nil {
		_ = tmux("kill-session", "-t", name).Run()
		return err
	}
	if q.Conversation != "" {
		if err = runTmux("set-option", "-t", name, "@jara_codex_thread", q.Conversation); err != nil {
			_ = tmux("kill-session", "-t", name).Run()
			return err
		}
	}
	var args []string
	if q.Background {
		if err = runTmux("set-option", "-t", name, "@jara_background", "1"); err != nil {
			_ = tmux("kill-session", "-t", name).Run()
			return err
		}
		if err = runTmux("set-window-option", "-t", name, "remain-on-exit", "on"); err != nil {
			_ = tmux("kill-session", "-t", name).Run()
			return err
		}
		args = []string{"respawn-pane", "-k", "-t", name, "mise", "exec", "--", "jara", "_background", token(q)}
	} else {
		terminalCommand := codexCommand(q.Resume, codexArgs)
		if q.Conversation != "" {
			terminalCommand, err = resumeConversationCommand(q.Agent, q.Conversation, q.ConversationCWD, codexArgs)
			if err != nil {
				_ = tmux("kill-session", "-t", name).Run()
				return err
			}
		}
		if q.ResumeLatest {
			terminalCommand = codexResumeLatestCommand(codexArgs)
		}
		if q.UserInterface == "jara" {
			terminalCommand = chatTerminalCommand(q, codexArgs)
			args = append([]string{"respawn-pane", "-k", "-t", name, "mise", "exec", "--"}, terminalCommand...)
		} else {
			args = []string{"respawn-pane", "-k", "-t", name, "mise", "exec", "--", "jara", "_terminal", token(terminalCommand)}
		}
	}
	if err = runTmux(args...); err != nil {
		_ = tmux("kill-session", "-t", name).Run()
		return fmt.Errorf("start terminal: %w", err)
	}
	if q.Background {
		backgroundPromptHandedOff = true
	}
	lifecycleUnlock()
	lifecycleLocked = false
	unlock()
	locked = false
	if q.Background {
		fmt.Fprintf(os.Stdout, "Started background Codex run in session %s. Use `jara sessions` to attach.\n", name)
		return nil
	}
	return attach(Session{Name: name, Workspace: q.ID}, q.Takeover)
}

func configureTmuxBackground(session string, background terminalRGB) error {
	if err := runTmux("set-window-option", "-t", session, "window-style", "bg="+background.hex()); err != nil {
		return err
	}
	return runTmux("set-environment", "-t", session, "JARA_TMUX_BACKGROUND", background.hex())
}

func runTmux(args ...string) error { cmd := tmux(args...); cmd.Stderr = os.Stderr; return cmd.Run() }

func attach(s Session, takeover bool) error {
	if err := markSessionAttached(s.Name); err != nil {
		return err
	}
	if err := configureTmuxStatus(s.Name); err != nil {
		return err
	}
	args := []string{"attach-session", "-t", s.Name}
	if takeover {
		args = append(args, "-d")
	}
	return interactive(tmux(args...))
}
