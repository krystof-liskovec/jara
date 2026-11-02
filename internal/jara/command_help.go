package jara

type helpEntry struct {
	usage    string
	summary  string
	commands []string
	options  []string
	examples []string
}

var commandHelp = map[string]helpEntry{
	"root": {
		usage:   "jara [COMMAND] [OPTIONS]",
		summary: "Open the current repository or browse persistent Codex workspaces.",
		commands: []string{
			"open, resume, new, run, connect, sessions\tManage remote Codex sessions.",
			"sync, edit, tools, doctor\tWork with a remote workspace.",
			"remote, workspace, config, auth\tManage workspaces and credentials.",
			"setup, deployment, host, uninstall\tSet up and maintain Jara hosts.",
			"forward, reverse, tunnel\tForward ports over SSH.",
			"menu/ui, version, keygen, key\tClient and SSH key tools.",
			"environment, migrate, help\tOther commands.",
		},
		options: []string{
			"--remote NAME       Select a configured host.",
			"--clone             Clone the current Git repository to the host.",
			"--copy              Copy the current folder to the host.",
			"--source DIR        Use another local folder.",
			"--codex-arg VALUE   Add a Codex argument; repeatable.",
			"-- CODEX_ARGS       Pass remaining arguments directly to Codex.",
		},
		examples: []string{
			"jara --clone --remote jellyfish",
			"jara --copy --source ~/projects/demo",
			"jara --codex-arg=--model --codex-arg=gpt-6.1-sol",
			"jara -- --model gpt-6.1-sol -c 'model_reasoning_effort=\"high\"'",
			"jara menu",
		},
	},
	"open": {
		usage:   "jara [open] [OPTIONS]",
		summary: "Open or create the session associated with the current folder.",
		options: []string{
			"--remote NAME       Select a configured host.",
			"--clone             Clone the current Git repository.",
			"--copy              Copy the current folder.",
			"--source DIR        Use another local folder.",
			"--codex-arg VALUE   Add a Codex argument; repeatable.",
			"-- CODEX_ARGS       Pass remaining arguments directly to Codex.",
		},
		examples: []string{
			"jara --clone --remote jellyfish",
			"jara --copy --source ~/projects/demo",
			"jara -- --model gpt-6.1-sol",
		},
	},
	"resume": {
		usage:   "jara resume [WORKSPACE OPTIONS] [-- CODEX_ARGS...]",
		summary: "Resume a Codex session in the current workspace.",
		options: []string{
			"--latest            Resume Codex's latest session instead of opening its picker.",
			"--remote NAME       Select a configured host.",
			"--source DIR        Use another local folder.",
			"--codex-arg VALUE   Add a Codex argument; repeatable.",
			"-- CODEX_ARGS       Pass remaining arguments directly to Codex.",
		},
		examples: []string{
			"jara resume --remote jellyfish",
			"jara resume --latest",
			"jara resume -- --all",
			"jara resume --codex-arg=--model --codex-arg=gpt-6.1-sol",
		},
	},
	"run": {
		usage:   "jara run [--resume-latest] [WORKSPACE OPTIONS] PROMPT [-- CODEX_ARGS...]",
		summary: "Run a Codex prompt in the background in the current workspace.",
		options: []string{
			"--resume-latest     Continue Codex's latest session with this prompt.",
			"--worktree          Run in a separate Git worktree.",
			"--clone, --copy     Create a remote workspace from this repository or folder.",
			"--source DIR        Use another local folder.",
			"--remote NAME       Select a configured host.",
			"--codex-arg VALUE   Add a Codex argument; repeatable.",
			"-- CODEX_ARGS       Pass remaining arguments directly to Codex.",
		},
		examples: []string{
			`jara run "Review this repository and report the top risks"`,
			`jara run --resume-latest "Apply the changes we discussed"`,
			`jara run --worktree --codex-arg=--full-auto "Implement the requested change"`,
		},
	},
	"new": {
		usage:   "jara new [--worktree] [WORKSPACE OPTIONS] [-- CODEX_ARGS...]",
		summary: "Start another Codex process in the current workspace.",
		options: []string{
			"--remote NAME       Select a configured host.",
			"--source DIR        Use another local folder.",
			"--worktree          Create a separate Git worktree.",
			"--takeover          Detach other clients from the selected session.",
			"--codex-arg VALUE   Add a Codex argument; repeatable.",
			"-- CODEX_ARGS       Pass remaining arguments directly to Codex.",
		},
		examples: []string{
			"jara new --worktree",
			"jara new --remote jellyfish -- --model gpt-6.1-sol",
		},
	},
	"connect": {
		usage:   "jara connect [OPTIONS] [WORKSPACE] [-- CODEX_ARGS...]",
		summary: "Open a workspace directly on the SSH server, without local repository transfer.",
		options: []string{
			"--resume            Resume Codex when starting a terminal.",
			"--new               Start another terminal.",
			"--worktree          Start in a new Git worktree.",
			"--takeover          Detach other terminal clients.",
			"--codex-arg VALUE   Add a Codex argument; repeatable.",
			"-- CODEX_ARGS       Pass remaining arguments directly to Codex.",
		},
		examples: []string{
			"jara connect",
			"jara connect w-0123456789abcdef01234567 --resume",
			"jara connect --new -- --model gpt-6.1-sol",
		},
	},
	"sessions": {
		usage:   "jara sessions [--remote NAME] [--codex-arg VALUE]...",
		summary: "Browse Codex conversations, workspaces and Jara terminals.",
		options: []string{
			"--remote NAME       Select a configured host.",
			"--codex-arg VALUE   Use when a new Codex process is started; repeatable.",
		},
		examples: []string{
			"jara sessions",
			"jara sessions --remote jellyfish",
		},
	},
	"edit": {
		usage:   "jara edit [--editor NAME] [WORKSPACE OPTIONS]",
		summary: "Open the current workspace in its configured remote editor.",
		options: []string{
			"--editor NAME       Override the configured editor (code, vim, nvim, or a custom command).",
			"--remote NAME       Select a configured host.",
			"--source DIR        Use another local folder.",
		},
		examples: []string{
			"jara edit",
			"jara edit --editor nvim --remote jellyfish",
		},
	},
	"sync": {
		usage:   "jara sync push|pull [OPTIONS]",
		summary: "Transfer changes between the local folder and its remote workspace.",
		options: []string{
			"--remote NAME       Select a configured host.",
			"--source DIR        Use another local folder.",
			"--force             Replace conflicting files.",
			"--include-ignored   Include Git-ignored files explicitly.",
			"--delete            Propagate previously tracked local deletions on push.",
		},
		examples: []string{
			"jara sync push",
			"jara sync pull --remote jellyfish --force",
			"jara sync push --include-ignored --delete",
		},
	},
	"tools": {
		usage:   "jara tools [current|install] [WORKSPACE OPTIONS]",
		summary: "Show or install project tool versions selected by mise.",
		options: []string{
			"--remote NAME       Select a configured host.",
			"--source DIR        Use another local folder.",
		},
		examples: []string{
			"jara tools current",
			"jara tools install --remote jellyfish",
		},
	},
	"doctor": {
		usage:   "jara doctor [--remote NAME]",
		summary: "Check local SSH and remote Jara setup.",
		options: []string{"--remote NAME       Select a configured host."},
		examples: []string{
			"jara doctor",
			"jara doctor --remote jellyfish",
		},
	},
	"forward": {
		usage:   "jara forward PORTS [OPTIONS]",
		summary: "Forward remote service ports to this machine.",
		options: []string{
			"--target HOST:PORT  Destination on the remote side; defaults to localhost and the same port.",
			"--bind ADDRESS      Local listener address (default 127.0.0.1).",
			"--remote NAME       Select a configured host.",
		},
		examples: []string{
			"jara forward 3000",
			"jara forward 3000,8000-8002 --target 127.0.0.1:3000 --bind 0.0.0.0",
		},
	},
	"reverse": {
		usage:   "jara reverse PORTS [OPTIONS]",
		summary: "Forward local service ports back to the remote environment.",
		options: []string{
			"--target HOST:PORT  Destination on this machine; defaults to localhost and the same port.",
			"--bind ADDRESS      Remote listener address (default 127.0.0.1).",
			"--remote NAME       Select a configured host.",
		},
		examples: []string{
			"jara reverse 5432",
			"jara reverse 3000-3002 --target 127.0.0.1:3000 --bind 0.0.0.0",
		},
	},
	"tunnel": {
		usage:   "jara tunnel [OPTIONS]",
		summary: "Open one SSH connection with local and reverse forwards.",
		options: []string{
			"--forward PORTS        Local forwards; repeatable.",
			"--reverse PORTS        Reverse forwards; repeatable.",
			"--forward-target HOST:PORT  Target for local forwards.",
			"--reverse-target HOST:PORT  Target for reverse forwards.",
			"--local-bind ADDRESS   Local listener address.",
			"--remote-bind ADDRESS  Remote listener address.",
			"--remote NAME          Select a configured host.",
		},
		examples: []string{
			"jara tunnel --forward 3000,8000-8002",
			"jara tunnel --reverse 5432 --remote-bind 0.0.0.0",
		},
	},
	"setup": {
		usage:   "jara setup",
		summary: "Interactively connect to a Jara host or deploy one on Docker.",
		examples: []string{
			"jara setup",
			"jara setup  # follow the prompts to configure Codex, Git access, and a workspace",
		},
	},
	"menu": {
		usage:    "jara menu",
		summary:  "Open the interactive client menu.",
		examples: []string{"jara menu"},
	},
	"ui": {
		usage:    "jara ui",
		summary:  "Compatibility alias for the interactive client menu.",
		examples: []string{"jara ui"},
	},
	"remote": {
		usage:   "jara remote add|list|use|remove ...",
		summary: "Manage named SSH connections.",
		examples: []string{
			"jara remote add jellyfish --host jellyfish --port 822 --user agent",
			"jara remote use jellyfish",
			"jara remote list",
		},
	},
	"remote add": {
		usage:   "jara remote add NAME --host HOST [OPTIONS]",
		summary: "Add a remote SSH host after checking its Jara setup.",
		options: []string{
			"--host HOST       SSH hostname or alias (required).",
			"--port PORT       SSH port; defaults to OpenSSH configuration.",
			"--user USER       SSH username.",
			"--identity FILE   SSH private key path.",
		},
		examples: []string{
			"jara remote add jellyfish --host jellyfish --port 822 --user agent",
			"jara remote add work --host server.example --identity ~/.ssh/id_ed25519_jara",
		},
	},
	"remote list":   {usage: "jara remote list", summary: "List configured remotes; * marks the default.", examples: []string{"jara remote list"}},
	"remote use":    {usage: "jara remote use NAME", summary: "Set the default remote.", examples: []string{"jara remote use jellyfish"}},
	"remote remove": {usage: "jara remote remove NAME", summary: "Forget a manually configured remote.", examples: []string{"jara remote remove old-server"}},
	"workspace": {
		usage:   "jara workspace list|delete ID [OPTIONS]",
		summary: "List or delete workspaces on a configured remote.",
		commands: []string{
			"list\tShow workspace IDs, names and repository origins.",
			"delete ID\tPermanently remove a workspace and its managed worktrees.",
		},
		examples: []string{
			"jara workspace list --remote jellyfish",
			"jara workspace delete --remote jellyfish w-0123456789abcdef01234567",
		},
	},
	"workspace list": {
		usage:    "jara workspace list [--remote NAME]",
		summary:  "List workspaces stored on a configured remote.",
		examples: []string{"jara workspace list", "jara workspace list --remote jellyfish"},
	},
	"workspace delete": {
		usage:    "jara workspace delete ID [--remote NAME]",
		summary:  "Permanently delete one workspace after confirmation; active workspaces are refused.",
		options:  []string{"--remote NAME   Select a configured host."},
		examples: []string{"jara workspace delete --remote jellyfish w-0123456789abcdef01234567"},
	},
	"config": {
		usage:   "jara config set|codex-args ...",
		summary: "Configure client preferences and host-level Codex defaults.",
		examples: []string{
			"jara config set editor nvim",
			"jara config codex-args show --remote jellyfish",
		},
	},
	"config set editor": {
		usage:   "jara config set editor code|vim|nvim [--remote NAME]",
		summary: "Choose the default editor, globally or for one remote.",
		options: []string{"--remote NAME   Save the editor for one remote instead of globally."},
		examples: []string{
			"jara config set editor nvim",
			"jara config set editor code --remote jellyfish",
		},
	},
	"config set editor-command": {
		usage:   "jara config set editor-command NAME JSON-ARRAY",
		summary: "Register a custom editor command. Use {ssh_alias} and {remote_path} placeholders.",
		examples: []string{
			"jara config set editor-command code '[\"code\",\"--remote\",\"ssh-remote+{ssh_alias}\",\"{remote_path}\"]'",
			"jara config set editor code",
		},
	},
	"config codex-args": {
		usage:   "jara config codex-args show|set|clear [OPTIONS]",
		summary: "Manage persistent Codex defaults on one Jara host.",
		examples: []string{
			"jara config codex-args show --remote jellyfish",
			"jara config codex-args set --remote jellyfish -- --model gpt-6.1-sol",
			"jara config codex-args clear --remote jellyfish",
		},
	},
	"config codex-args show":  {usage: "jara config codex-args show [--remote NAME]", summary: "Show effective host and deployment defaults.", options: []string{"--remote NAME   Select a configured host."}, examples: []string{"jara config codex-args show --remote jellyfish"}},
	"config codex-args set":   {usage: "jara config codex-args set [--remote NAME] -- CODEX_ARGS...", summary: "Replace saved host defaults; arguments after -- are passed literally.", options: []string{"--remote NAME   Select a configured host."}, examples: []string{"jara config codex-args set --remote jellyfish -- --model gpt-6.1-sol -c 'model_reasoning_effort=\"high\"'"}},
	"config codex-args clear": {usage: "jara config codex-args clear [--remote NAME]", summary: "Remove saved host defaults and restore deployment defaults.", options: []string{"--remote NAME   Select a configured host."}, examples: []string{"jara config codex-args clear --remote jellyfish"}},
	"auth": {
		usage:   "jara auth codex|git|gitlab|gitea [OPTIONS]",
		summary: "Configure remote Codex sign-in or Git credentials.",
		examples: []string{
			"jara auth codex --remote jellyfish",
			"jara auth git --remote jellyfish",
		},
	},
	"auth codex": {usage: "jara auth codex [--remote NAME]", summary: "Sign in to Codex on a remote host using device authorization.", options: []string{"--remote NAME   Select a configured host."}, examples: []string{"jara auth codex --remote jellyfish"}},
	"auth git": {
		usage:   "jara auth git [OPTIONS]",
		summary: "Configure Git credentials for a repository host.",
		options: []string{
			"--remote NAME       Select a configured host.",
			"--username USER     Username for --token-stdin.",
			"--token-stdin       Read the token from standard input.",
		},
		examples: []string{
			"jara auth git --remote jellyfish",
			"printf '%s' \"$GIT_TOKEN\" | jara auth git --remote jellyfish --token-stdin --username oauth2",
		},
	},
	"auth gitlab": {usage: "jara auth gitlab [--remote NAME] [--host HOST] --token-stdin", summary: "Save a GitLab token on a remote host.", options: []string{"--remote NAME   Select a configured host.", "--host HOST     GitLab hostname (default gitlab.com).", "--token-stdin   Read the token from standard input."}, examples: []string{"printf '%s' \"$GIT_TOKEN\" | jara auth gitlab --remote jellyfish --token-stdin"}},
	"auth gitea":  {usage: "jara auth gitea [--remote NAME] --url HTTPS_URL --token-stdin", summary: "Save a Gitea token on a remote host.", options: []string{"--remote NAME   Select a configured host.", "--url URL       Gitea HTTPS base URL.", "--token-stdin   Read the token from standard input."}, examples: []string{"printf '%s' \"$GITEA_TOKEN\" | jara auth gitea --remote jellyfish --url https://git.example --token-stdin"}},
	"keygen": {
		usage:    "jara keygen [--out FILE]",
		summary:  "Generate a Jara SSH key pair and print its public key.",
		options:  []string{"--out FILE   Private key output path (default ~/.ssh/id_ed25519_jara)."},
		examples: []string{"jara keygen", "jara keygen --out ~/.ssh/jara-work"},
	},
	"key": {
		usage:   "jara key install|list|remove [OPTIONS]",
		summary: "Manage SSH public keys on a guided Jara deployment.",
		commands: []string{
			"install\tAdd a public key to a remote deployment.",
			"list\tShow its authorized key fingerprints.",
			"remove\tRevoke a key by fingerprint.",
		},
		examples: []string{"jara key install --remote work --public-key ~/.ssh/phone.pub"},
	},
	"key install": {
		usage:   "jara key install [OPTIONS]",
		summary: "Add a public SSH key to a guided deployment without restarting its container.",
		options: []string{
			"--remote NAME       Select a configured guided deployment.",
			"--public-key FILE   Public key file containing one SSH key (required).",
			"--connect-only      Force this key to open Jara's workspace menu.",
		},
		examples: []string{"jara key install --remote work --public-key ~/.ssh/phone.pub", "jara key install --remote work --public-key ~/.ssh/termius.pub --connect-only"},
	},
	"key list": {
		usage:    "jara key list [--remote NAME]",
		summary:  "List authorized SSH key fingerprints on a guided deployment.",
		examples: []string{"jara key list --remote work"},
	},
	"key remove": {
		usage:    "jara key remove --fingerprint SHA256:... [--remote NAME]",
		summary:  "Remove an authorized SSH key by fingerprint; at least one key must remain.",
		options:  []string{"--fingerprint VALUE   Fingerprint shown by jara key list."},
		examples: []string{"jara key remove --remote work --fingerprint SHA256:abc123..."},
	},
	"editor": {usage: "jara editor import vim|nvim [OPTIONS]", summary: "Import a local editor configuration into a remote workspace.", examples: []string{"jara editor import nvim --source ~/.config/nvim --remote jellyfish"}},
	"editor import": {
		usage:   "jara editor import vim|nvim --source DIR [OPTIONS]",
		summary: "Import Vim or Neovim configuration into the selected remote host.",
		options: []string{
			"--source DIR    Local configuration directory (required).",
			"--remote NAME   Select a configured host.",
			"--force         Replace conflicting remote files.",
		},
		examples: []string{
			"jara editor import nvim --source ~/.config/nvim --remote jellyfish",
			"jara editor import vim --source ~/.vim --force",
		},
	},
	"deployment": {
		usage:   "jara deployment upgrade|uninstall [NAME] [OPTIONS]",
		summary: "Upgrade or remove a Docker deployment created by Jara setup.",
		examples: []string{
			"jara deployment upgrade jellyfish",
			"jara deployment uninstall jellyfish",
		},
	},
	"deployment upgrade": {
		usage:    "jara deployment upgrade [NAME]",
		summary:  "Build and validate this CLI's Jara source bundle on the Docker host.",
		examples: []string{"jara deployment upgrade jellyfish"},
	},
	"deployment uninstall": {
		usage:   "jara deployment uninstall [NAME] [--volumes]",
		summary: "Remove a guided Jara deployment; volumes are preserved by default.",
		options: []string{"--volumes   Also permanently delete workspace, login, Git, and host-key volumes."},
		examples: []string{
			"jara deployment uninstall jellyfish",
			"jara deployment uninstall jellyfish --volumes",
		},
	},
	"uninstall": {
		usage:    "jara uninstall [NAME] [--volumes]",
		summary:  "Compatibility alias for removing a guided Jara deployment.",
		options:  []string{"--volumes   Also permanently delete workspace, login, Git, and host-key volumes."},
		examples: []string{"jara uninstall jellyfish"},
	},
	"host": {usage: "jara host update [OPTIONS]", summary: "Update Codex on a Linux Docker host.", examples: []string{"jara host update --project jara-jellyfish --check"}},
	"host update": {
		usage:   "jara host update --project NAME [OPTIONS]",
		summary: "Update the host's Codex version while preserving Jara data.",
		options: []string{
			"--project NAME      Deployment project name (required).",
			"--native            Use the native Docker deployment manifest.",
			"--compose FILE      Compose file path.",
			"--compose-extra FILE  Optional Compose overlay.",
			"--env-file FILE     Deployment environment file.",
			"--native-manifest FILE  Native Docker deployment manifest.",
			"--check             Report an update without deploying it.",
			"--version VERSION   Pin an explicit Codex version.",
		},
		examples: []string{
			"jara host update --project jara-jellyfish --check",
			"jara host update --project jara-jellyfish --version 0.166.0",
		},
	},
	"host upgrade": {
		usage:    "jara host upgrade [NAME]",
		summary:  "Compatibility alias for upgrading a guided Jara deployment.",
		examples: []string{"jara host upgrade jellyfish"},
	},
	"environment": {
		usage:   "jara environment [OPTIONS]",
		summary: "Generate an environment file for a Compose deployment.",
		options: []string{
			"--out FILE          Output path (created without overwriting).",
			"--ssh-user NAME     SSH username inside the environment (default agent).",
			"--max-sessions N    Maximum concurrent terminals (1-500, default 500).",
			"--session-idle-timeout DURATION  Close detached sessions after this long (default 24h; 0 disables).",
			"--codex-version VER Exact Codex version; defaults to latest stable.",
			"--node-version VER  Exact Node version.",
			"--go-version VER    Optional exact Go version.",
			"--vim, --nvim       Install the selected editor.",
			"--glab-version VER  Optional GitLab CLI version.",
			"--tea-version VER   Optional Gitea CLI version.",
			"--mise-version VER  Exact mise version.",
			"--docker            Enable rootless Docker sidecar support.",
			"--docker-version VER  Docker CLI and daemon version.",
		},
		examples: []string{
			"jara environment --out .env",
			"jara environment --ssh-user jara --out .env",
			"jara environment --out .env --codex-version 0.166.0 --nvim",
		},
	},
	"migrate": {usage: "jara migrate", summary: "Show migration availability for native Codex conversations.", examples: []string{"jara migrate"}},
	"version": {usage: "jara version", summary: "Print the Jara version.", examples: []string{"jara version"}},
}
