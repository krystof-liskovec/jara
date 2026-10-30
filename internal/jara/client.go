package jara

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

func validateOrigin(origin string) error {
	if origin == "" || strings.HasPrefix(origin, "-") || strings.ContainsAny(origin, "\x00\r\n") {
		return fmt.Errorf("invalid origin URL")
	}
	if u, err := url.Parse(origin); err == nil && (u.Scheme == "https" || u.Scheme == "http") && (u.User != nil || u.RawQuery != "") {
		return fmt.Errorf("origin must not embed credentials or query parameters; configure a remote Git credential helper")
	}
	return nil
}

func confirm(prompt string) (bool, error) {
	fmt.Fprint(os.Stderr, prompt+" [y/N] ")
	s, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes", nil
}
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}
func workspaceRoot(cwd string, copyMode bool) (string, string, bool) {
	if !copyMode {
		if root, err := gitOutput(cwd, "rev-parse", "--show-toplevel"); err == nil {
			rel, e := filepath.Rel(root, cwd)
			if e == nil {
				return root, rel, true
			}
		}
	}
	return cwd, ".", false
}

func workspaceContext(c Config, cwd string, copyMode bool) (string, string, bool) {
	root, rel, isGit := workspaceRoot(cwd, copyMode)
	if copyMode {
		return root, rel, isGit
	}
	for candidate := cwd; ; candidate = filepath.Dir(candidate) {
		if _, ok := c.Bindings[candidate]; ok {
			relative, err := filepath.Rel(candidate, cwd)
			if err == nil {
				return candidate, relative, isGit
			}
		}
		if (isGit && candidate == root) || filepath.Dir(candidate) == candidate {
			break
		}
	}
	return root, rel, isGit
}
func remoteCommand(c *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("use remote add, list, or use")
	}
	switch args[0] {
	case "list":
		names := make([]string, 0, len(c.Remotes))
		for n := range c.Remotes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			mark := " "
			if n == c.Default {
				mark = "*"
			}
			fmt.Printf("%s %s\t%s\n", mark, n, c.Remotes[n].Host)
		}
		return nil
	case "use":
		if len(args) != 2 {
			return fmt.Errorf("use remote use NAME")
		}
		if _, ok := c.Remotes[args[1]]; !ok {
			return fmt.Errorf("unknown remote")
		}
		c.Default = args[1]
		return saveConfig(*c)
	case "add":
		if len(args) < 2 || !nameRE.MatchString(args[1]) {
			return fmt.Errorf("a name containing letters, digits, underscores or hyphens is required")
		}
		f := flag.NewFlagSet("remote add", flag.ContinueOnError)
		r := Remote{}
		f.StringVar(&r.Host, "host", "", "SSH host or alias")
		f.IntVar(&r.Port, "port", 0, "SSH port (default: SSH configuration)")
		f.StringVar(&r.User, "user", "", "SSH user")
		f.StringVar(&r.Identity, "identity", "", "private key path")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return fmt.Errorf("unexpected argument")
		}
		if err := validateRemote(r); err != nil {
			return err
		}
		if err := doctor(r); err != nil {
			return err
		}
		c.Remotes[args[1]] = r
		if c.Default == "" {
			c.Default = args[1]
		}
		return saveConfig(*c)
	}
	return fmt.Errorf("unknown remote command")
}
func configCommand(c *Config, args []string) error {
	if len(args) == 4 && args[0] == "set" && args[1] == "editor-command" {
		if !nameRE.MatchString(args[2]) {
			return fmt.Errorf("invalid editor name")
		}
		var argv []string
		if err := json.Unmarshal([]byte(args[3]), &argv); err != nil || len(argv) == 0 || argv[0] == "" {
			return fmt.Errorf("editor-command requires a nonempty JSON array of strings")
		}
		if c.EditorCommands == nil {
			c.EditorCommands = map[string][]string{}
		}
		c.EditorCommands[args[2]] = argv
		return saveConfig(*c)
	}
	if len(args) < 3 || args[0] != "set" || args[1] != "editor" {
		return fmt.Errorf("use config set editor code|vim|nvim [--remote NAME]")
	}
	e := args[2]
	if e != "code" && e != "vim" && e != "nvim" && len(c.EditorCommands[e]) == 0 {
		return fmt.Errorf("unsupported editor")
	}
	if len(args) == 3 {
		c.Editor = e
	} else if len(args) == 5 && args[3] == "--remote" {
		r, ok := c.Remotes[args[4]]
		if !ok {
			return fmt.Errorf("unknown remote")
		}
		r.Editor = e
		c.Remotes[args[4]] = r
	} else {
		return fmt.Errorf("invalid editor arguments")
	}
	return saveConfig(*c)
}
func doctor(r Remote) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("install the OpenSSH client: %w", err)
	}
	var d Diagnosis
	if err := rpc(r, Request{Op: "doctor"}, nil, &d); err != nil {
		return err
	}
	if d.Protocol != Protocol {
		return fmt.Errorf("Jara protocol mismatch: client %d, remote %d; update both", Protocol, d.Protocol)
	}
	fmt.Printf("Jara %s; protocol %d; storage %s\n", d.Version, d.Protocol, d.Root)
	for _, s := range d.Checks {
		fmt.Println(s)
	}
	if !d.OK {
		return fmt.Errorf("remote needs setup; see diagnostics above")
	}
	return nil
}
func forward(r Remote, direction, port, target, bind string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port must be 1–65535")
	}
	if strings.ContainsAny(bind, "\n\r") || bind == "" {
		return fmt.Errorf("invalid bind address")
	}
	if target == "" {
		target = "127.0.0.1:" + port
	}
	if strings.ContainsAny(target, "\n\r") {
		return fmt.Errorf("invalid destination")
	}
	flag := "-L"
	if direction == "reverse" {
		flag = "-R"
	}
	host, targetPort, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return fmt.Errorf("target must be HOST:PORT (bracket IPv6 addresses)")
	}
	tn, e := strconv.Atoi(targetPort)
	if e != nil || tn < 1 || tn > 65535 {
		return fmt.Errorf("target port must be 1–65535")
	}
	spec := net.JoinHostPort(strings.Trim(bind, "[]"), port) + ":" + target
	a := append(sshArgs(r, false), "-N", "-o", "ExitOnForwardFailure=yes", flag, spec, "--", r.Host)
	fmt.Fprintf(os.Stderr, "%s listening on %s:%s → %s; Ctrl-C closes tunnel\n", direction, bind, port, target)
	return interactive(exec.Command("ssh", a...))
}
func editorAlias(name string, r Remote) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ssh")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	alias := "jara-" + name
	// Include is managed separately so existing user configuration is preserved.
	generated := filepath.Join(dir, "jara_config")
	c, err := readConfig()
	if err != nil {
		return "", err
	}
	c.Remotes[name] = r
	names := make([]string, 0, len(c.Remotes))
	for n := range c.Remotes {
		names = append(names, n)
	}
	sort.Strings(names)
	var out strings.Builder
	out.WriteString("# Generated by jara edit.\n")
	for _, n := range names {
		v := c.Remotes[n]
		if err = validateRemote(v); err != nil {
			return "", err
		}
		effective, err := exec.Command("ssh", append(sshArgs(v, false), "-G", "--", v.Host)...).Output()
		if err != nil {
			return "", fmt.Errorf("resolve SSH configuration for %s: %w", n, err)
		}
		fmt.Fprintf(&out, "Host jara-%s\n", n)
		allowed := map[string]bool{"hostname": true, "user": true, "port": true, "identityfile": true, "certificatefile": true, "proxyjump": true, "proxycommand": true, "identitiesonly": true, "identityagent": true, "userknownhostsfile": true, "globalknownhostsfile": true, "hostkeyalias": true, "stricthostkeychecking": true, "addressfamily": true}
		for _, line := range strings.Split(string(effective), "\n") {
			key, _, ok := strings.Cut(line, " ")
			if ok && allowed[key] {
				out.WriteString("  " + line + "\n")
			}
		}
		out.WriteString("\n")
	}
	if err = os.WriteFile(generated, []byte(out.String()), 0600); err != nil {
		return "", err
	}
	config := filepath.Join(dir, "config")
	existing, err := os.ReadFile(config)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	include := "Include jara_config"
	if !strings.Contains(string(existing), include) {
		existing = append([]byte(include+"\n"), existing...)
		if err = os.WriteFile(config, existing, 0600); err != nil {
			return "", err
		}
	}
	return alias, nil
}
