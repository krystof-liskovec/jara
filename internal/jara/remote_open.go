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

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

func connectCommand(args []string) error {
	args, tail := splitCodexArgs(args)
	f := flag.NewFlagSet("connect", flag.ContinueOnError)
	var codexArgs codexArgList
	f.Var(&codexArgs, "codex-arg", "additional Codex argument (repeatable)")
	chatInterface := f.String("interface", "jara", "conversation interface: jara or codex")
	agentName := f.String("agent", os.Getenv("JARA_AGENT"), "agent runtime: codex or claude")
	resume := f.Bool("resume", false, "use plain codex resume when starting a terminal")
	fresh := f.Bool("new", false, "start another Codex terminal")
	worktree := f.Bool("worktree", false, "start a new terminal in a Git worktree")
	takeover := f.Bool("takeover", false, "detach other clients when attaching")
	last := f.Bool("last", false, "attach to the most recently used running session")
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
	if *last && id == "" {
		// Phones reconnect often (network changes, the app suspended); go
		// straight back to the session, or to the menu when none is running.
		id = lastActiveWorkspace()
	}
	runtime, err := protocol.NormalizeAgent(*agentName)
	if err != nil {
		return err
	}
	explicit := false
	f.Visit(func(set *flag.Flag) { explicit = explicit || set.Name == "agent" })
	return remoteOpen(Request{UserInterface: *chatInterface, Runtime: runtime, AgentExplicit: explicit, ID: id, Resume: *resume, New: *fresh || *worktree, Worktree: *worktree, Takeover: *takeover, CodexArgs: append(codexArgs, tail...)})
}

func remoteOpen(q Request) error {
	if err := validateChatInterface(q.UserInterface); err != nil {
		return err
	}
	if q.Handoff && q.Runtime == "" {
		q.Runtime = handoffTarget(q.ID)
	}
	runtime, err := protocol.NormalizeAgent(q.Runtime)
	if err != nil {
		return err
	}
	q.Runtime = runtime
	if err = validateAgentInterface(q.Runtime, q.UserInterface); err != nil {
		return err
	}
	claude := q.Runtime == protocol.AgentClaude
	if q.Background && strings.TrimSpace(q.Prompt) == "" {
		return fmt.Errorf("background %s runs require a prompt", protocol.AgentDisplayName(q.Runtime))
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
	// Each agent receives only its own deployment and host default arguments.
	var codexArgs []string
	if claude && q.UserInterface == "jara" {
		// The Jara UI's app-server keeps Codex defaults. Arguments given for
		// Claude Code travel separately to the agent router.
		if len(q.CodexArgs) > 0 {
			q.AgentArgs, q.CodexArgs = q.CodexArgs, nil
		}
		codexArgs, err = configuredCodexArgs(nil)
	} else if claude {
		codexArgs, err = configuredAgentArgs(protocol.AgentClaude, q.CodexArgs)
	} else {
		codexArgs, err = configuredCodexArgs(q.CodexArgs)
	}
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
			command := codexWithoutSharedDaemon(codexResumeAllCommand(codexArgs))
			if claude {
				command = claudeCommand(true, false, codexArgs)
			}
			return interactive(exec.Command(command[0], command[1:]...))
		}
		if selected.Kind == "conversation" {
			// The saved conversation decides which agent resumes it.
			if runtime := conversationRuntime(selected.Agent, selected.Action); runtime != q.Runtime {
				q.Runtime = runtime
				defaults := runtime
				if q.UserInterface == "jara" {
					defaults = protocol.AgentCodex
				}
				// Extra arguments were meant for the other agent; use defaults.
				if codexArgs, err = configuredAgentArgs(defaults, nil); err != nil {
					return err
				}
			}
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
				resume := exec.Command(command[0], command[1:]...)
				resume.Dir = selected.Path // Claude Code finds sessions by working directory
				return interactive(resume)
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
			return attach(Session{Name: selected.Action, Workspace: selected.Workspace}, q.Takeover, q.RemoteName)
		}

		q.ID = selected.Workspace
		q.New = q.New || selected.Kind == "new"
		if selected.Kind == "new" && selected.Agent != "" {
			q.AgentExplicit = true
		}
		if selected.Kind == "new" && selected.Agent != "" && selected.Agent != q.Runtime {
			q.Runtime = selected.Agent
			if err = validateAgentInterface(q.Runtime, q.UserInterface); err != nil {
				return err
			}
			runtimeArgs := q.Runtime
			if q.UserInterface == "jara" {
				runtimeArgs = protocol.AgentCodex // the Jara UI's app-server always takes Codex defaults
			}
			if codexArgs, err = configuredAgentArgs(runtimeArgs, q.CodexArgs); err != nil {
				return err
			}
		}
	}
	if q.UserInterface == "jara" && q.ID != "" && !q.Background {
		if connection, err := hubRunnerRoute(&q); err != nil {
			return err
		} else if connection != nil {
			return runRemoteChatClientConnection(q, connection)
		}
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
			// The handed-off conversation decides which agent resumes it.
			if runtime := conversationRuntime(q.Agent, q.Conversation); runtime != q.Runtime {
				q.Runtime = runtime
				if codexArgs, err = configuredAgentArgs(runtime, nil); err != nil {
					return err
				}
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
		return attach(matches[i], q.Takeover, q.RemoteName)
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
		path = filepath.Join(fsutil.DataRoot(), "worktrees", name)
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
	if q.Conversation != "" && !q.Worktree {
		if info, e := os.Stat(q.ConversationCWD); e == nil && info.IsDir() {
			path = q.ConversationCWD // resume where the conversation ran
		}
	}
	if info, e := os.Stat(path); e != nil || !info.IsDir() {
		return fmt.Errorf("remote working directory is missing: %s", path)
	}
	if err = prepareToolsQuiet(path); err != nil {
		return err
	}
	if q.Handoff {
		if q.Background || q.UserInterface == "jara" {
			return fmt.Errorf("a handoff starts an interactive agent terminal")
		}
		q.Prompt = handoffPrompt(path, handoffSource(q.ID), q.Runtime, q.Prompt)
	}
	ensureDelegationRegistered()
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
	if err = runTmux("set-option", "-t", name, "@jara_agent", q.Runtime); err != nil {
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
		terminalCommand, err := agentTerminalCommand(q, codexArgs)
		if err != nil {
			_ = tmux("kill-session", "-t", name).Run()
			return err
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
		fmt.Fprintf(os.Stdout, "Started background %s run in session %s. Use `jara sessions` to attach.\n", protocol.AgentDisplayName(q.Runtime), name)
		return nil
	}
	return attach(Session{Name: name, Workspace: q.ID}, q.Takeover, q.RemoteName)
}

func configureTmuxBackground(session string, background terminalRGB) error {
	if err := runTmux("set-window-option", "-t", session, "window-style", "bg="+background.hex()); err != nil {
		return err
	}
	return runTmux("set-environment", "-t", session, "JARA_TMUX_BACKGROUND", background.hex())
}
