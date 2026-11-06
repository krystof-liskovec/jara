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
	"strings"
)

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
		if selected.Kind == "chat-daemon" {
			if q.Background {
				return fmt.Errorf("a persistent Jara conversation is already running; reconnect with `jara connect --interface jara`")
			}
			if q.UserInterface == "jara" {
				return runRemoteChatClient(Request{UserInterface: "jara", ID: selected.Workspace})
			}
			q.ID = selected.Workspace
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
	if q.UserInterface == "jara" && q.ID != "" && !q.Background {
		connection, managed, err := ensureChatDaemon(q)
		if err != nil {
			return err
		}
		if !managed {
			return runRemoteChatClientConnection(q, connection)
		}
	}
	if q.UserInterface != "jara" && q.ID != "" {
		daemonWasRunning := chatDaemonRunning(q.ID)
		state, found, handoffErr := prepareChatDaemonForCodex(q.ID)
		if handoffErr != nil {
			return handoffErr
		}
		if found {
			if q.Conversation == "" {
				q.Conversation = state.Conversation
			}
			if q.ConversationCWD == "" {
				if info, statErr := os.Stat(state.CWD); statErr == nil && info.IsDir() {
					q.ConversationCWD = state.CWD
				}
			}
			if q.Agent == "" {
				q.Agent = "Codex"
			}
			q.ResumeLatest = false
			q.New = q.New || daemonWasRunning
		}
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
