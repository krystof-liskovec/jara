# Jara

**Just Another Remote Agent**

Persistent remote Codex workspaces over standard OpenSSH. The host runs Docker;
Compose remains an option for manual deployments and Docker-in-Docker. The local
Go CLI remembers environments and workspace associations.

Jara launches the actual Codex terminal interface inside tmux. It is not an agent
harness. Ordinary SSH, SFTP, VS Code Remote SSH, and port forwarding remain
available to the environment owner.

Codex startup arguments are configurable through `JARA_CODEX_ARGS` in the
container and arguments forwarded by the local Jara client. No extra Codex flags
are enabled by default.

**Security status:** a focused review found a startup privilege escalation,
which has been fixed and regression tested. Treat this as a development prototype
until remaining items in the [security review](docs/security-review.md) are
addressed; the optional privileged Docker sidecar is for trusted workloads.

## Build the client

Requires Go 1.27.3 or newer and an OpenSSH client (`ssh`). Git is required locally
for repository discovery, Git-aware exclusions, and cloning unpublished commits.
No local Docker or rsync is required for everyday use.

```sh
./install.sh
```

This installs `jara` into `~/.local/bin` and does not modify your shell `PATH`.
If that directory is not already on your `PATH`, run it as `~/.local/bin/jara`.
The installer refreshes the deployment source bundle embedded in the CLI.

## First connection

Run `jara setup` on your computer to reuse a saved remote or deploy Jara to a new
Linux Docker host, then check access and sign in to Codex. It detects saved
remotes automatically; for a running Jara server not in your config, use
`jara remote add`. New deployments run over SSH when your account can run Docker
and the host has `tar`. The guide can generate a dedicated service key or use
an existing SSH key, transfers the source bundled in the CLI, and uses native
`docker build` and `docker run`; no Git
checkout or Compose plugin is needed on the host. Docker installation and
firewall rules remain host-specific; see the detailed
[host setup](#deploy-on-a-linux-docker-host) if you need to prepare those yourself.
On Debian or Ubuntu with Docker's default AppArmor profile, guided setup detects
it and configures the Jara container automatically; no host profile or Docker
daemon changes are needed for Codex's sandbox. Setup waits for Jara's SSH service
to answer before running its deployment check.
For a noninteractive client setup, use:

```sh
jara remote add work --host my-agent-alias
jara auth codex --remote work
```

From a Git repository, run `jara --remote work --clone` to open it remotely.
For a non-Git folder, use `jara --remote work --copy`. After that, `jara` in
the same folder reopens the workspace. For a host without an SSH alias, include
`--port`, `--user`, and `--identity` in `remote add` as needed. The detailed
steps below cover host keys, direct SSH access, and other setup options.
Run `jara menu` to open an interactive client menu for workspaces, remotes,
authentication, diagnostics, and guided deployments. Bare `jara` still opens
the workspace for the current folder.

Remove a host deployed by the guide with `jara deployment uninstall work`. It
stops the container and removes deployment files while keeping workspace data,
Codex login, and Git credentials in Docker volumes. Add `--volumes` only when you
also want to permanently delete that data, for example
`jara deployment uninstall work --volumes`. This also removes the server host
key, so verify the new fingerprint after a later reinstall. Uninstall asks
separately whether to remove a Jara-generated local
SSH key pair; user-provided keys are kept. `jara remote remove NAME` only
forgets a manually configured remote.

The client also builds for Windows and macOS. Native terminal/editor behavior on
those platforms needs validation; cross-compilation alone does not establish it.

## Deploy on a Linux Docker host

For manual deployments, check out this repository on the host and use Docker
Compose. The guided `jara setup` path uses native Docker commands instead.

```sh
~/.local/bin/jara environment --out deploy/.env
# Put your PUBLIC SSH key(s) in deploy/authorized_keys.
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build
```

Without `--codex-version`, Jara resolves npm's latest stable Codex release and
records that exact version in `deploy/.env`. Rebuilds stay pinned to that version;
use the host updater to select a newer one.
Pass `--codex-version VERSION` when you want a specific release. Resolving latest
requires network access to the npm registry.

The generated configuration binds SSH to host loopback on port 2222. To connect
directly from another machine, set `JARA_SSH_BIND` to the host's reachable interface
address and allow that port, or use an existing SSH jump host/tunnel. Guided host
deployment defaults to `0.0.0.0` so the service can be reached remotely; restrict
port 2222 with the host firewall to the networks you trust.

Before starting a managed session, Jara asks the connected terminal for its
background color and passes it to tmux and Codex's PTY relay. This restores
Codex's shaded composer while matching the user's terminal theme, including for
plain SSH clients that support OSC 11. If the terminal does not answer, Jara uses
`#262626`; `JARA_TMUX_BACKGROUND` remains available as an optional override.
Running Codex sessions need to be restarted to pick up a new color.

Print the host fingerprint through your trusted host access:

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml exec agent \
  ssh-keygen -lf /var/lib/jara/ssh/ssh_host_ed25519_key.pub
```

Verify it against the first connection prompt. Host keys and `/home/agent` live in
named volumes. Rebuilds preserve files and conversation history; processes stop
when their container stops. Do not use `down -v` if you want to retain this data.

For multiple containers, use distinct Compose project names (`-p personal`,
`-p work`) and SSH ports. Each project gets separate volumes and host keys.
Set `AUTHORIZED_KEYS_FILE` to select a different public-key file per environment.
Agents run as `agent`, without sudo or access to the host Docker socket.
Optional [Docker-in-Docker support](docs/docker.md) adds a separate rootless
daemon per environment, Docker Compose and Buildx. Enable it with
`jara environment --docker` and the documented Compose overlay.
The Docker deployment uses a scoped seccomp profile and permits the nested mount
namespace setup Codex needs for its workspace sandbox. Codex permissions follow
its configuration and any explicitly supplied startup arguments. See
[verification and sandbox details](docs/verification.md).

## Plain SSH and phones

Connect from Termius or another SSH client as `agent`, then run `jara connect` for
the workspace/running-session browser. No Jara installation is needed on the phone.
Set Termius's host Startup Command to `jara connect` to open it on connection.
The browser adapts to terminal size, shows readable project names and session
status, and supports single-key selection plus keyboard navigation and paging.
See [phone setup, keys and direct workspace commands](docs/plain-ssh.md).

## Register and connect

```sh
jara remote add work --host example.org --port 2222 --user agent --identity /path/to/key
jara remote list
jara remote use work
jara doctor
jara auth codex
```

Existing SSH aliases and jump hosts work: `--host my-agent-alias`. `JARA_SSH_CONFIG`
selects an alternative OpenSSH configuration file. The default configuration is
`jara/config.json` in the OS user configuration directory; `JARA_CONFIG`
overrides it.
For a guided deployment, use `jara key install --remote work --public-key ~/.ssh/phone.pub`
after generating the pair with `jara keygen --out ~/.ssh/phone`. Manual Compose
deployments still use `deploy/authorized_keys`. Passphrase setup is handled by OpenSSH.
Concurrent configuration updates fail rather than overwrite each other's changes; retry
the command. An interrupted config update may leave
a `config.json.lock` directory; remove it only after confirming no Jara writer runs.

From a repository:

```sh
jara                 # Offer initial clone, subsequently reopen
jara --clone         # Skip initial clone confirmation
jara resume          # Attach if running, otherwise plain codex resume
jara --remote work   # Select and remember this remote for this checkout
jara new --worktree  # Additional session in a separate Git worktree
```

Cloning reproduces the local branch and commit, including unpublished history via
a Git bundle. Private repositories need remote Git credentials; run
`jara auth git --remote NAME` before cloning to import or enter them. Local
working edits are not part of the Git bundle. Before starting a session, Jara
asks whether to sync local changes that have not already been transferred.
Ignored files remain excluded. If local deletions are pending, the prompt says
that accepting will also remove those files remotely. Launching from a
repository subdirectory preserves that relative path.

From another folder:

```sh
jara --copy          # Initial file copy, then open Codex
jara                 # Reopen the associated copied workspace
jara sessions        # Remote workspace and running-terminal browser
```

Detach using **Ctrl-b d**. Disconnecting SSH also leaves the tmux terminal running.
The bottom status bar shows the workspace, window tabs, and detach shortcut in a
theme that matches the terminal background, with contrasting text and teal accents.
Reconnecting applies the theme to existing sessions too.
Opening it again attaches rather than starting a duplicate. Several matches show
a selector. Multiple clients can share the terminal; `--takeover` detaches others.
`jara new` explicitly starts another session in the same files, after a notice.

## Additional Codex arguments

Guided setup can save default Codex arguments on the Jara host, one argument per
prompt. You can inspect, replace, or clear them later. Upgrade existing guided
deployments once with `jara deployment upgrade NAME` to add this host setting:

```sh
jara config codex-args show --remote jellyfish
jara config codex-args set --remote jellyfish -- --model gpt-6.1-sol
jara config codex-args clear --remote jellyfish
```

Arguments are stored in the host's persistent Jara data. `set` replaces the
host's saved defaults; `clear` returns to the deployment default. The settings
apply when a new Codex process starts. Existing sessions keep their arguments.

For manual deployments, `JARA_CODEX_ARGS` in the host deployment env file
remains the default source. It accepts a JSON array of literal arguments. For
example, to explicitly enable YOLO mode:

```dotenv
JARA_CODEX_ARGS='["--dangerously-bypass-approvals-and-sandbox"]'
```

The default is `[]`. The array is validated at startup and is available to both
client connections and plain SSH `jara connect`. Values are never evaluated as
shell commands.

The client can also forward arguments for the current invocation:

```sh
jara -- --dangerously-bypass-approvals-and-sandbox
jara --codex-arg=--model --codex-arg=gpt-6.1-sol
jara resume -- --all
jara new -- --model gpt-6.1-sol -c 'model_reasoning_effort="high"'
```

Saved host arguments replace the deployment default, followed by repeatable
`--codex-arg` values and then tokens after `--`. Codex handles validation and
duplicate options.
For `resume`, these arguments follow the `resume` subcommand. They apply only
when a new Codex process starts; attaching to an existing session keeps that
process's arguments. Use `jara new` for another session or exit Codex and reopen.
Plain SSH also supports `jara connect WORKSPACE_ID -- CODEX_ARGS...`.

Opting into `--dangerously-bypass-approvals-and-sandbox` removes Codex's approval
prompts and workspace sandbox. Codex can then access other workspaces and
credentials available to the container's `agent` account. See the
[security review](docs/security-review.md).

## Paste clipboard images

With the local Jara client on Linux or macOS, copy an image and press **Ctrl+V**
in Codex's composer. Jara reads the local clipboard, uploads the image over SSH,
and attaches it to the draft without pressing Enter. Existing text stays in the
composer. Use the terminal's normal text paste shortcut for text.

Linux needs `wl-paste` from `wl-clipboard` on Wayland or `xclip` on X11, installed
on the client machine. macOS uses its built-in `osascript`. Only PNG/JPEG images
up to 20 MiB and 40 million pixels are accepted. The upload uses a second SSH
connection; encrypted keys must be available through `ssh-agent` or another
noninteractive SSH authentication method.

Attachments stay in the remote `~/.local/share/jara/attachments` directory so
saved Codex conversations can retain their image paths. They are not synchronized
back into the local repository or removed automatically. Clipboard capture runs
only when you press the shortcut. Set `JARA_IMAGE_PASTE=0` locally to disable it.

Native Windows and plain SSH/Termius connections do not have this local clipboard
bridge. Upload an image file with SFTP/SCP and paste its remote path there.
The macOS clipboard backend needs native validation; automated coverage currently
exercises Linux and the actual Codex composer with a fixture clipboard provider.

## Multiple Node and Go versions

Projects can select independent toolchains in one container using `mise.toml`:

```toml
[tools]
node = "20.20.0"
go = "1.26.0"
```

Another project can select different versions and run concurrently. Jara installs
missing versions before starting Codex, with a persistent shared installation
cache. `.nvmrc`, `.node-version`, `.go-version` and supported `go.mod`/`go.work`
directives also work. Codex's own launcher keeps its fixed image Node runtime.

Use `jara tools` to inspect a workspace's selected versions and `jara tools install`
to prepare them without opening Codex. [Project toolchain details](docs/project-toolchains.md)
cover SSH/editor behavior, version-file semantics and deployment.

## Transfer changes explicitly

```sh
jara sync push
jara sync pull
jara sync push --force  # Explicitly choose source on conflicts / override busy guard
jara sync push --delete # Propagate deletions of previously synchronized files
jara sync push --include-ignored # Explicitly include Git-ignored files
```

Reopening never automatically synchronizes or switches branches. Transfer tracks
content hashes and previous endpoint states. Conflicts abort before writes;
destination-only changes and files are preserved. Transfers into an active
Jara-managed workspace require an explicit override. Editor writes are not locked:
avoid editing the destination during transfer.

Sync hashes a metadata manifest for every eligible file on each side, then sends
content only for files selected by the sync plan. Scanning still reads every
eligible file to compute its hash, so disk work grows with the workspace size.
Older hosts use the previous full-snapshot path until upgraded with
`jara deployment upgrade`.
Current protocol limits: 64 MiB of changed file content per sync, held in memory;
regular files and internal relative symlinks; no empty
directory replication, Git index transfer, submodule transfer, or Git LFS object
management. A failed I/O operation can leave earlier individual file updates in
place; each regular file replacement is atomic, the whole batch is not.

Git repositories use `git ls-files --cached --others --exclude-standard`.
`.jaraignore` supports `*`, `?`, `**`, root anchoring, directory
prefixes, comments, and ordered `!` exceptions. It is a documented subset of
gitignore syntax (no bracket patterns or escaped whitespace). In non-Git folders,
all regular files are eligible unless excluded; exclude credentials and generated
directories explicitly. `.git` metadata is never copied.

## Editors

```sh
jara config set editor code
jara config set editor nvim --remote work
jara edit
jara edit --editor vim
```

VS Code needs its local `code` command and Remote SSH extension. Jara generates
`~/.ssh/jara_config` and adds an Include to `~/.ssh/config`. Vim/Neovim must be
selected in the environment build and run remotely.
Import a configuration directory with conflict protection:

```sh
jara editor import nvim --source /path/to/nvim-config --remote work
jara editor import vim --source /path/to/vim-config --remote work
```

Neovim configuration goes to `~/.config/nvim`; Vim configuration goes to `~/.vim`
(use a `vimrc` file there). Plugins and their dependencies are not installed
automatically. Missing editor binaries produce host setup instructions.

Other local editors can use an explicit argument array; no shell interpolation:

```sh
jara config set editor-command custom '["my-editor","--ssh","{ssh_alias}","{remote_path}"]'
jara config set editor custom
```

## Port forwarding

```sh
jara forward 3000,8000-8002
jara reverse 5432,7000-7002
jara tunnel --forward 3000,8000-8002 --reverse 5432,7000-7002
jara forward 8080 --target 127.0.0.1:3000
```

Each port forwards to the same port on `127.0.0.1` at the other end by default.
Comma lists and inclusive ranges work on `forward` and `reverse`; `tunnel` starts
both directions in one SSH connection. Repeat `--forward` or `--reverse` as
needed. `--forward-target` and `--reverse-target` set a common destination for
that direction; `--target` does the same for `jara forward` or `jara reverse`.
Each direction accepts at most 256 ports. Jara defaults listeners
to loopback, and the SSH server enforces loopback for reverse listeners too. To
allow other hosts to reach reverse listeners, set
`JARA_GATEWAY_PORTS=clientspecified` in the host environment file and recreate
the container; then pass `--remote-bind` explicitly. `--local-bind` controls
laptop-side forward listeners. Ctrl-C closes all tunnels without stopping Codex.

## Optional tools and provider tokens

```sh
jara environment --go-version 1.27.3 \
  --node-version 22.22.0 --vim --nvim --glab-version 1.126.0 \
  --tea-version 0.16.0 --out deploy/.env
```

Existing output files are never overwritten. Host owners update the saved file
and rebuild. Application/tool versions are recorded; Debian package repositories
are not snapshot-pinned, so builds are not byte-for-byte reproducible.

Optional automatic Codex updates run on the Docker host. Guided deployments use
native Docker and need no Compose plugin; run the command from the bundled
deployment directory:

```sh
cd ~/.jara-deploy-personal
jara host update --project jara-personal --check
```

New guided deployments use `~/.jara-deploy-NAME`. Existing deployments keep
their recorded directory; upgrade and uninstall continue to support it.

Existing Compose deployments can continue to use:

```sh
jara host update --project jara-personal --compose deploy/compose.yaml \
  --env-file deploy/.env --check
```

Omit `--check` to apply an available stable update. The updater builds and tests a
candidate, defers while terminals or SSH connections are open, blocks new Jara
operations during replacement, and records the successful exact version in the
env file. [Enable the optional hourly timer](docs/automatic-updates.md) to automate
this. No timer is enabled by default.

To update Jara itself on a host created by guided setup, run this from the local
client after installing the newer CLI:

```sh
jara deployment upgrade work
```

It transfers the source embedded in the CLI, builds and validates a replacement
with Docker on the host, defers while the environment is in use, and rolls back
if the new container fails. The host needs no Git, Compose, or Jara installation.
Workspace data and the Codex version pin are preserved. After a successful
upgrade, Jara asks whether to remove the previous image used for rollback.

To clone a private repository and push from the remote container, run this from
the checkout before cloning:

```sh
jara auth git --remote work
```

Jara imports the current checkout's local Git credential when available, or asks
for a username and hidden token. Guided setup offers this before an initial
clone. HTTPS and standard SSH origins are supported; SSH origins are converted
to HTTPS for the remote workspace, leaving the local origin unchanged. Custom SSH
aliases or ports need an HTTPS origin.
The token is stored in the remote's persistent home with mode `0600` and is shared
by repositories on that Git instance. Its provider permissions determine which
repositories it can access, and processes running as the container's `agent`
account can use it. Use a least-privilege token. To supply it from a secure file
instead of the prompt, use:

```sh
jara auth git --remote work --username USER --token-stdin < /secure/git-token
```

Provider CLI tokens can also be configured explicitly through stdin rather than
command arguments:

```sh
jara auth gitlab --host gitlab.example.org --token-stdin < /secure/gitlab-token
jara auth gitea --url https://gitea.example.org --token-stdin < /secure/gitea-token
```

Alternatively set `GITLAB_TOKEN_FILE` / `GITEA_TOKEN_FILE` on the Compose host to
absolute token-file paths, and set `GITLAB_HOST` / `GITEA_URL`. Startup configures
the selected provider CLI and HTTPS Git credential helper. Install that CLI in
the image first. Tokens are stored by the provider in the persistent agent home;
they are not build arguments or image-layer inputs. Repeated startup with the
same token is skipped. To rotate Tea's token, remove its managed `jara` login first.
Direct token setup needs real-provider validation; automated tests use no tokens.

## Status and verification

Native conversation migration is **unavailable**, with an explicit error and no
source changes. See [migration findings](docs/migration.md). No summary fallback
is substituted automatically.

```sh
go test -race ./...
go vet ./...
docker build -f deploy/Dockerfile --build-arg CODEX_VERSION=0.166.0 -t jara-agent:validation .
python3 scripts/e2e.py
# Also exercise editor configuration import with the optional-tool image:
# JARA_TEST_IMAGE=jara-agent:tools-validation python3 scripts/e2e.py
```

The transport smoke test uses temporary keys and a fake Codex process. For the
actual Compose deployment, real Codex UI, disconnect/reconnect, sandbox execution
and volume persistence, run `python3 scripts/real_codex_e2e.py` after building
`jara-agent:real-validation`. It needs no credentials or inference. An optional
`--live-auth PATH` explicitly enables a separate live turn.

One authorized live turn has also been verified; see the [verification report](docs/verification.md)
for the exact scope and the sandbox issue it exposed. [Agreed design](docs/design.md)
distinguishes the target behavior from remaining implementation work.
