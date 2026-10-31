package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const help = `Jara — Just Another Remote Agent

Jara runs persistent remote Codex workspaces over OpenSSH.

Sessions and workspaces:
  jara [--remote NAME] [--clone | --copy] [--source DIR]  Open Codex
  jara resume [workspace options]                         Resume a session, without --last
  jara new [--worktree] [workspace options]                Start a new session
  jara connect [--resume] [--new] [--worktree] [--takeover] [WORKSPACE]
                                                          Open directly on the SSH server
  jara sessions [--remote NAME]                           Browse remote workspaces and sessions
  jara tools [install] [workspace options]                Show or install project tool versions
  jara edit [--editor code|vim|nvim] [workspace options]
  jara sync push|pull [--force] [workspace options]
  Session commands accept --codex-arg=VALUE (repeatable), or -- CODEX_ARGS...

Remote connections:
  jara remote add NAME --host HOST [--port PORT] [--user USER] [--identity FILE]
  jara remote list | use NAME | remove NAME

Setup and deployments:
  jara setup                                               Guided connection or Docker deployment
  jara deployment upgrade [NAME]                          Upgrade Jara on a guided deployment
  jara deployment uninstall [NAME] [--volumes]            Remove a guided deployment

Host maintenance:
  jara host update --project NAME [--native] [--compose FILE]
                  [--compose-extra FILE] [--env-file FILE] [--check] [--version VERSION]
    Update Codex on the Linux Docker host.

Configuration and authentication:
  jara config set editor code|vim|nvim [--remote NAME]
  jara config set editor-command NAME '["command","{ssh_alias}","{remote_path}"]'
  jara keygen [--out FILE]                            Generate a key and install its public key
  jara auth codex|gitlab|gitea                        Provider authentication
  jara auth git [--remote NAME] [--token-stdin] [--username USER]
                                                      Configure Git credentials for this instance
  jara editor import vim|nvim --source DIR [--remote NAME] [--force]

Networking and diagnostics:
  jara doctor [--remote NAME]                         Check local or remote setup
  jara forward|reverse PORTS [--target HOST:PORT] [--bind ADDRESS] [--remote NAME]
  jara tunnel [--forward PORTS] [--reverse PORTS] [--local-bind ADDRESS]
             [--remote-bind ADDRESS] [--remote NAME]

Environment:
  jara environment [--codex-version VERSION] [--node-version VERSION]
                  [--go-version VERSION] [--vim] [--nvim]
                  [--glab-version VERSION] [--tea-version VERSION]
                  [--mise-version VERSION] [--docker] [--docker-version VERSION]
  jara migrate                        Explain native migration availability

PORTS accepts comma-separated ports and inclusive ranges; each direction is limited to 256.
OpenSSH must be installed locally. See README.md for Compose deployment.
Transfers support --include-ignored and explicit --delete on sync.
`

func Run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "_rpc", "_open":
			if len(args) != 2 {
				return fmt.Errorf("invalid internal invocation")
			}
			var q Request
			if err := decode(args[1], &q); err != nil {
				return err
			}
			if args[0] == "_rpc" {
				return remoteRPC(q)
			}
			return remoteOpen(q)
		case "_terminal":
			if len(args) != 2 {
				return fmt.Errorf("invalid internal invocation")
			}
			var codexArgs []string
			if err := decode(args[1], &codexArgs); err != nil {
				return err
			}
			return runCodexTerminal(codexArgs)
		case "help", "--help", "-h":
			fmt.Print(help)
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
	clone := f.Bool("clone", false, "clone without confirmation")
	copyMode := f.Bool("copy", false, "copy current directory")
	source := f.String("source", "", "source directory")
	editor := f.String("editor", "", "editor override")
	newTree := f.Bool("worktree", false, "create a separate Git worktree")
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
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	codexArgs = append(codexArgs, tail...)
	if err = validateCodexArgs(codexArgs); err != nil {
		return err
	}
	if len(codexArgs) > 0 && action != "open" && action != "new" && action != "resume" && action != "sessions" {
		return fmt.Errorf("Codex arguments require open, new, resume, or sessions")
	}
	switch action {
	case "open", "resume", "new", "sessions", "edit", "sync", "doctor", "forward", "reverse", "tunnel", "tools":
	default:
		return fmt.Errorf("unknown command %q; see jara --help", action)
	}
	if *clone && *copyMode {
		return fmt.Errorf("--clone and --copy are mutually exclusive")
	}
	if *newTree && action != "new" {
		return fmt.Errorf("--worktree requires jara new")
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
	openSession := func(q Request) error { q.CodexArgs = codexArgs; return openRemote(r, q) }
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
	key := chosen + "\n" + root
	b, exists := c.Associations[key]
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
		if !*copyMode && !*clone {
			yes, e := confirm("Clone this repository to " + chosen + "?")
			if e != nil {
				return e
			}
			if !yes {
				return openSession(Request{})
			}
		}
		sum := sha256.Sum256([]byte(root))
		id := "w-" + hex.EncodeToString(sum[:12])
		q := Request{Op: "create", ID: id, Name: filepath.Base(root)}
		if !*copyMode {
			q.URL, err = gitOutput(root, "remote", "get-url", "origin")
			if err != nil {
				return fmt.Errorf("repository needs an origin remote; use --copy instead")
			}
			q.Commit, err = gitOutput(root, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			q.Branch, _ = gitOutput(root, "symbolic-ref", "--short", "-q", "HEAD")
			if err = validateOrigin(q.URL); err != nil {
				return err
			}
			if isSSHGitOrigin(q.URL) {
				target, e := parseGitCredentialTarget(q.URL)
				if e != nil {
					return e
				}
				q.URL = target.URL
			}
			if status, e := gitOutput(root, "status", "--porcelain"); e == nil && status != "" {
				fmt.Fprintln(os.Stderr, "Local edits are excluded from clone. Transfer them explicitly with jara sync push after initialization.")
			}
		}
		if err = rpc(r, q, nil, nil); err != nil {
			if q.URL != "" {
				return fmt.Errorf("%w\nIf this is a private repository, run `jara auth git --remote %s` from the checkout and retry.", err, chosen)
			}
			return err
		}
		b = Binding{Remote: chosen, Workspace: id}
		if !*copyMode {
			bundle, cleanup, e := repositoryBundle(root)
			if e != nil {
				return e
			}
			defer cleanup()
			if e = rpc(r, Request{Op: "checkout", ID: id, Branch: q.Branch, Commit: q.Commit}, bundle, nil); e != nil {
				return e
			}
			if e = seedCloneBaseline(r, b, root); e != nil {
				fmt.Fprintln(os.Stderr, "Clone succeeded but initial sync baseline could not be recorded:", e)
			}
		}
		if *copyMode {
			if err = syncWorkspace(r, b, root, "push", false, *includeIgnored, false); err != nil {
				return err
			}
			rel = "."
		}
		c.Associations[key] = b
	}
	c.Bindings[root] = b
	if err = saveConfig(c); err != nil {
		return err
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
