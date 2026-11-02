package jara

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Run(args []string) error {
	if showCommandHelp(args) {
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "_rpc", "_open":
			if len(args) != 2 {
				return fmt.Errorf("invalid internal invocation")
			}
			if args[0] == "_open" {
				return openRequest(args[1])
			}
			var q Request
			if err := decode(args[1], &q); err != nil {
				return err
			}
			return remoteRPC(q)
		case "_terminal":
			if len(args) != 2 {
				return fmt.Errorf("invalid internal invocation")
			}
			var codexArgs []string
			if err := decode(args[1], &codexArgs); err != nil {
				return err
			}
			return runCodexTerminal(codexArgs)
		case "_background":
			if len(args) != 2 {
				return fmt.Errorf("invalid background Codex invocation")
			}
			var q Request
			if err := decode(args[1], &q); err != nil {
				return err
			}
			return runBackgroundCodex(q)
		case "help", "--help", "-h":
			printHelpEntry("root", commandHelp["root"])
			return nil
		case "version", "--version":
			fmt.Println(Version)
			return nil
		case "connect":
			return connectCommand(args[1:])
		case "setup":
			if len(args) != 1 {
				return fmt.Errorf("use jara setup")
			}
			c, err := readConfig()
			if err != nil {
				return err
			}
			return guidedSetup(c)
		case "menu", "ui":
			c, err := readConfig()
			if err != nil {
				return err
			}
			return clientMenu(c)
		case "deployment":
			c, err := readConfig()
			if err != nil {
				return err
			}
			return deploymentCommand(&c, args[1:])
		case "uninstall":
			// Keep the original spelling as a compatibility alias.
			c, err := readConfig()
			if err != nil {
				return err
			}
			return uninstallCommand(&c, args[1:])
		case "environment":
			return environment(args[1:])
		case "keygen":
			return keygen(args[1:])
		case "key":
			c, err := readConfig()
			if err != nil {
				return err
			}
			return keyCommand(c, args[1:])
		case "host":
			if len(args) > 1 && args[1] == "upgrade" {
				// Keep the original spelling as a compatibility alias.
				c, err := readConfig()
				if err != nil {
					return err
				}
				return deploymentUpgradeCommand(c, args[2:])
			}
			return hostCommand(args[1:])
		case "_maintenance":
			return maintenanceCommand(args[1:])
		case "_session-janitor":
			return sessionJanitorCommand(args[1:])
		case "_session-attached", "_session-count":
			return sessionUtilityCommand(args[0], args[1:])
		case "_validate-session-settings":
			return validateSessionSettings()
		case "_tools":
			return toolsCommand(args[1:])
		case "_auth":
			if len(args) != 3 && len(args) != 4 {
				return fmt.Errorf("invalid auth invocation")
			}
			return remoteAuth(args[1], args[2], args[3:]...)
		case "_validate-codex-args":
			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
			if err != nil {
				return err
			}
			_, err = parseCodexArgs(raw)
			return err
		case "migrate":
			return fmt.Errorf("native conversation migration is unavailable: Codex 0.166.0 has no verified supported CLI import interface; source conversation was not changed (see docs/migration.md)")
		}
	}
	c, err := readConfig()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "remote" {
		return remoteCommand(&c, args[1:])
	}
	if len(args) > 0 && args[0] == "workspace" {
		return workspaceCommand(&c, args[1:])
	}
	if len(args) > 0 && args[0] == "config" {
		return configCommand(&c, args[1:])
	}
	if len(args) > 0 && args[0] == "auth" {
		return authCommand(c, args[1:])
	}
	if len(args) > 0 && args[0] == "editor" {
		return editorImport(c, args[1:])
	}
	action := "open"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		args = args[1:]
	}
	direction := ""
	toolsAction := "current"
	if action == "tools" && len(args) > 0 && (args[0] == "install" || args[0] == "current") {
		toolsAction = args[0]
		args = args[1:]
	}
	if action == "sync" {
		if len(args) == 0 {
			return fmt.Errorf("use jara sync push or jara sync pull")
		}
		direction = args[0]
		args = args[1:]
		if direction != "push" && direction != "pull" {
			return fmt.Errorf("sync direction must be push or pull")
		}
	}
	port := ""
	if action == "forward" || action == "reverse" {
		if len(args) == 0 {
			return fmt.Errorf("port or port range is required")
		}
		port = args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet(action, flag.ContinueOnError)
	args, tail := splitCodexArgs(args)
	var codexArgs codexArgList
	f.Var(&codexArgs, "codex-arg", "additional Codex argument (repeatable)")
	remoteName := f.String("remote", "", "named remote")
	clone := f.Bool("clone", false, "skip workspace discovery and clone confirmation")
	chooseWorkspace := f.Bool("choose-workspace", false, "choose an existing checkout for this local folder")
	copyMode := f.Bool("copy", false, "copy current directory")
	source := f.String("source", "", "source directory")
	editor := f.String("editor", "", "editor override")
	newTree := f.Bool("worktree", false, "create a separate Git worktree")
	latest := f.Bool("latest", false, "resume Codex's latest session")
	resumeLatest := f.Bool("resume-latest", false, "continue Codex's latest session with a prompt")
	takeover := f.Bool("takeover", false, "detach other terminal clients")
	force := f.Bool("force", false, "overwrite conflicts / permit transfer with running agents")
	includeIgnored := f.Bool("include-ignored", false, "explicitly include Git-ignored files; .jaraignore still applies")
	deleteMissing := f.Bool("delete", false, "propagate deletions of previously synchronized files")
	var target, bind *string
	var tunnelForwards, tunnelReverses portSpecs
	var forwardTarget, reverseTarget, localBind, remoteBind *string
	if action == "forward" || action == "reverse" {
		target = f.String("target", "", "forward destination host:port")
		bind = f.String("bind", "127.0.0.1", "listener address")
	}
	if action == "tunnel" {
		f.Var(&tunnelForwards, "forward", "local forward ports/ranges, e.g. 3000,8000-8002 (repeatable)")
		f.Var(&tunnelReverses, "reverse", "reverse forward ports/ranges, e.g. 5432,9000-9002 (repeatable)")
		forwardTarget = f.String("forward-target", "", "destination for every local forward; default: same localhost port")
		reverseTarget = f.String("reverse-target", "", "destination for every reverse forward; default: same localhost port")
		localBind = f.String("local-bind", "127.0.0.1", "laptop listener address for local forwards")
		remoteBind = f.String("remote-bind", "127.0.0.1", "container listener address for reverse forwards")
	}
	if err = f.Parse(args); err != nil {
		return err
	}
	prompt := ""
	if action == "run" {
		prompt = strings.Join(f.Args(), " ")
		if strings.TrimSpace(prompt) == "" {
			return fmt.Errorf("use jara run [--resume-latest] [OPTIONS] PROMPT")
		}
		if len(prompt) > maxBackgroundPrompt {
			return fmt.Errorf("prompt exceeds 64 KiB")
		}
	} else if f.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	codexArgs = append(codexArgs, tail...)
	if err = validateCodexArgs(codexArgs); err != nil {
		return err
	}
	if len(codexArgs) > 0 && action != "open" && action != "new" && action != "resume" && action != "run" && action != "sessions" {
		return fmt.Errorf("Codex arguments require open, new, resume, run, or sessions")
	}
	switch action {
	case "open", "resume", "new", "run", "sessions", "edit", "sync", "doctor", "forward", "reverse", "tunnel", "tools":
	default:
		return fmt.Errorf("unknown command %q; see jara --help", action)
	}
	if *latest && action != "resume" {
		return fmt.Errorf("--latest requires jara resume")
	}
	if *resumeLatest && action != "run" {
		return fmt.Errorf("--resume-latest requires jara run")
	}
	if *clone && *copyMode {
		return fmt.Errorf("--clone and --copy are mutually exclusive")
	}
	if *chooseWorkspace && (*clone || *copyMode || action != "open" && action != "new" && action != "resume" && action != "run") {
		return fmt.Errorf("--choose-workspace requires open, new, resume, or run without --clone or --copy")
	}
	if *newTree && action != "new" && action != "run" {
		return fmt.Errorf("--worktree requires jara new or jara run")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if *source != "" {
		cwd, err = filepath.Abs(*source)
		if err != nil {
			return err
		}
	}
	if resolved, e := filepath.EvalSymlinks(cwd); e == nil {
		cwd = resolved
	}
	if info, e := os.Stat(cwd); e != nil || !info.IsDir() {
		return fmt.Errorf("source must be an existing directory: %s", cwd)
	}
	root, rel, isGit := workspaceContext(c, cwd, *copyMode)
	if *chooseWorkspace && !isGit {
		return fmt.Errorf("run --choose-workspace from a Git repository")
	}
	if resolved, e := filepath.EvalSymlinks(root); e == nil {
		root = resolved
	}
	chosen := *remoteName
	if chosen == "" {
		chosen = c.Bindings[root].Remote
	}
	if chosen == "" {
		chosen = c.Default
	}
	r, ok := c.Remotes[chosen]
	if !ok {
		return fmt.Errorf("no selected remote; use jara remote add NAME --host HOST, then jara remote use NAME")
	}
	if err = validateRemote(r); err != nil {
		return err
	}
	openSession := func(q Request) error {
		q.CodexArgs = codexArgs
		if action == "resume" {
			q.ResumeLatest = *latest
		}
		if action == "run" {
			q.New = true
			q.Worktree = *newTree
			q.Background = true
			q.Prompt = prompt
			q.ResumeLatest = *resumeLatest
		}
		return openRemote(r, q)
	}
	if action == "doctor" {
		return doctor(chosen, r)
	}
	if action == "forward" || action == "reverse" {
		return forward(r, action, port, *target, *bind)
	}
	if action == "tunnel" {
		forwards, e := parsePortSpecs(tunnelForwards)
		if e != nil {
			return fmt.Errorf("--forward: %w", e)
		}
		reverses, e := parsePortSpecs(tunnelReverses)
		if e != nil {
			return fmt.Errorf("--reverse: %w", e)
		}
		return openTunnel(r, forwards, reverses, *forwardTarget, *reverseTarget, *localBind, *remoteBind)
	}
	if action == "sessions" {
		return openSession(Request{})
	}
	origin := ""
	var originErr error
	if isGit {
		origin, originErr = gitRepositoryOrigin(root)
	}
	legacyKey := workspaceAssociationKey(chosen, root, "")
	key := workspaceAssociationKey(chosen, root, origin)
	b, exists := c.Associations[key]
	if !exists && origin != "" {
		if legacy, ok := c.Associations[legacyKey]; ok {
			if existing, matches := workspaceForOrigin(r, legacy.Workspace, origin); matches && existing.Ready {
				b, exists = legacy, true
			}
		}
	}
	if *chooseWorkspace {
		exists = false
	}
	var clonePromptReader *bufio.Reader
	if !exists && !isGit && !*copyMode {
		if action != "open" && action != "resume" {
			return fmt.Errorf("no workspace association; first use jara --copy or open a Git repository")
		}
		return openSession(Request{Resume: action == "resume"})
	}
	if !exists {
		if action == "sync" || action == "edit" || action == "tools" {
			return fmt.Errorf("workspace is not associated with %s; open it with jara first", chosen)
		}
		if !*copyMode && originErr != nil {
			return originErr
		}
		var choice existingCheckoutChoice
		if !*copyMode && !*clone {
			choice, err = offerExistingCheckout(r, chosen, root, origin)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
		if choice.Binding.Workspace != "" {
			b = choice.Binding
		} else {
			if !*copyMode && !*clone && !choice.Clone {
				clonePromptReader = bufio.NewReader(os.Stdin)
				yes, e := confirmWithReader(clonePromptReader, "Clone this repository to "+chosen+"?")
				if e != nil {
					return e
				}
				if !yes {
					return openSession(Request{})
				}
			}
			b, err = createClientWorkspace(r, chosen, root, origin, *copyMode, *includeIgnored, choice.Clone)
			if err != nil {
				return err
			}
		}
		if *copyMode {
			rel = "."
		}
		c.Associations[key] = b
	}
	if origin != "" {
		c.Associations[key] = b
		c.Associations[legacyKey] = b
	}
	c.Bindings[root] = b
	if err = saveConfig(c); err != nil {
		return err
	}
	if action == "open" || action == "new" || action == "resume" || action == "run" {
		if err = syncLocalChangesBeforeSession(r, b, root, chosen, *includeIgnored, isGit, clonePromptReader); err != nil {
			return err
		}
	}
	if action == "sync" {
		return syncWorkspace(r, b, root, direction, *force, *includeIgnored, *deleteMissing)
	}
	if isGit {
		var info Workspace
		if err = rpc(r, Request{Op: "info", ID: b.Workspace}, nil, &info); err != nil {
			return err
		}
		localBranch, _ := gitOutput(root, "symbolic-ref", "--short", "-q", "HEAD")
		if localBranch != info.Branch {
			fmt.Fprintf(os.Stderr, "Branch differs: local %q, remote %q; remote checkout preserved.\n", localBranch, info.Branch)
		}
	}
	q := Request{ID: b.Workspace, Subdir: filepath.ToSlash(rel), Resume: action == "resume", New: action == "new", Worktree: *newTree, Takeover: *takeover}
	if action == "edit" || action == "tools" {
		var info Workspace
		if err = rpc(r, Request{Op: "info", ID: b.Workspace}, nil, &info); err != nil {
			return err
		}
		selected := *editor
		if selected == "" {
			selected = r.Editor
		}
		if selected == "" {
			selected = c.Editor
		}
		if selected == "" {
			selected = "code"
		}
		remotePath := info.Path
		if rel != "." {
			remotePath += "/" + filepath.ToSlash(rel)
		}
		if action == "tools" {
			return interactive(sshCommand(r, toolsAction == "install", shellArgs("jara", "_tools", toolsAction, remotePath)))
		}
		if err = interactive(sshCommand(r, true, shellArgs("jara", "_tools", "install", remotePath))); err != nil {
			return err
		}
		if selected == "vim" || selected == "nvim" {
			return interactive(sshCommand(r, true, shellArgs("jara", "_tools", "exec", remotePath, selected, "--", remotePath)))
		}
		alias, err := editorAlias(chosen, r)
		if err != nil {
			return err
		}
		if selected != "code" {
			argv, ok := c.EditorCommands[selected]
			if !ok || len(argv) == 0 {
				return fmt.Errorf("unknown editor %q; configure an editor-command argument array", selected)
			}
			expanded := make([]string, len(argv))
			for i, arg := range argv {
				expanded[i] = strings.NewReplacer("{ssh_alias}", alias, "{remote_path}", remotePath).Replace(arg)
			}
			return interactive(exec.Command(expanded[0], expanded[1:]...))
		}
		return interactive(exec.Command("code", "--remote", "ssh-remote+"+alias, remotePath))
	}
	return openSession(q)
}
