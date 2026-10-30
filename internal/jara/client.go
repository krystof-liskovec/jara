package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const help = `Jara — Just Another Remote Agent

  jara — persistent remote Codex workspaces over OpenSSH

  jara [--remote NAME] [--clone | --copy] [--source DIR]
  jara resume [workspace options]      Run codex resume, without --last
  jara new [--worktree] [workspace options]
  jara connect [--resume] [--new] [--worktree] [--takeover] [WORKSPACE]
                                       Open sessions directly on the SSH server
  jara sessions [--remote NAME]         Remote workspace and session browser
  jara tools [install] [workspace options]  Show or install this project's tool versions
  jara edit [--editor code|vim|nvim] [workspace options]
  jara sync push|pull [--force] [workspace options]
  jara remote add NAME --host HOST [--port PORT] [--user USER] [--identity FILE]
  jara remote list | use NAME
  jara config set editor code|vim|nvim [--remote NAME]
  jara config set editor-command NAME '["command","{ssh_alias}","{remote_path}"]'
  jara keygen [--out FILE]             Generate a key; install its public key on host
  jara auth codex|gitlab|gitea          Guided auth; provider tokens use --token-stdin
  jara editor import vim|nvim --source DIR [--remote NAME] [--force]
  jara doctor [--remote NAME]
  jara forward|reverse PORT [--target HOST:PORT] [--bind ADDRESS] [--remote NAME]
  jara environment --codex-version VERSION [--node-version VERSION]
                  [--go-version VERSION] [--vim] [--nvim]
                  [--glab-version VERSION] [--tea-version VERSION]
                  [--mise-version VERSION] [--docker] [--docker-version VERSION]
  jara migrate                        Explain native migration availability
  jara host update --project NAME [--compose deploy/compose.yaml]
                  [--compose-extra FILE] [--env-file deploy/.env] [--check] [--version VERSION]

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
		case "help", "--help", "-h":
			fmt.Print(help)
			return nil
		case "version", "--version":
			fmt.Println(Version)
			return nil
		case "connect":
			return connectCommand(args[1:])
		case "environment":
			return environment(args[1:])
		case "keygen":
			return keygen(args[1:])
		case "host":
			return hostCommand(args[1:])
		case "_maintenance":
			return maintenanceCommand(args[1:])
		case "_tools":
			return toolsCommand(args[1:])
		case "_auth":
			if len(args) != 3 {
				return fmt.Errorf("invalid auth invocation")
			}
			return remoteAuth(args[1], args[2])
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
			return fmt.Errorf("port is required")
		}
		port = args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet(action, flag.ContinueOnError)
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
	target := f.String("target", "", "forward destination host:port")
	bind := f.String("bind", "127.0.0.1", "listener address")
	if err = f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	switch action {
	case "open", "resume", "new", "sessions", "edit", "sync", "doctor", "forward", "reverse", "tools":
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
	if action == "doctor" {
		return doctor(r)
	}
	if action == "forward" || action == "reverse" {
		return forward(r, action, port, *target, *bind)
	}
	if action == "sessions" {
		return openRemote(r, Request{})
	}
	key := chosen + "\n" + root
	b, exists := c.Associations[key]
	if !exists && !isGit && !*copyMode {
		if action != "open" && action != "resume" {
			return fmt.Errorf("no workspace association; first use jara --copy or open a Git repository")
		}
		return openRemote(r, Request{Resume: action == "resume"})
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
				return openRemote(r, Request{})
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
			if status, e := gitOutput(root, "status", "--porcelain"); e == nil && status != "" {
				fmt.Fprintln(os.Stderr, "Local edits are excluded from clone. Transfer them explicitly with jara sync push after initialization.")
			}
		}
		if err = rpc(r, q, nil, nil); err != nil {
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
	return openRemote(r, q)
}
