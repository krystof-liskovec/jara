# Jag

Persistent remote Codex workspaces over standard OpenSSH. The host runs Docker
Compose; the local Go CLI remembers environments and workspace associations.

Jag launches the actual Codex terminal interface inside tmux. It is not an agent
harness. Ordinary SSH, SFTP, VS Code Remote SSH, and port forwarding remain
available to the environment owner.

**Security status:** a focused review found a startup privilege escalation,
which has been fixed and regression tested. Treat this as a development prototype
until remaining items in the [security review](docs/security-review.md) are
addressed; the optional privileged Docker sidecar is for trusted workloads.

## Build the client

Requires Go 1.27.3 or newer and an OpenSSH client (`ssh`). Git is required locally
for repository discovery, Git-aware exclusions, and cloning unpublished commits.
No local Docker or rsync is required for everyday use.

```sh
go build -o bin/jag ./cmd/jag
```

The client also builds for Windows and macOS. Native terminal/editor behavior on
those platforms needs validation; cross-compilation alone does not establish it.

## Deploy on a Linux Docker host

Check out this repository on the host, then:

```sh
./bin/jag environment --codex-version 0.166.0 --out deploy/.env
# Put your PUBLIC SSH key(s) in deploy/authorized_keys.
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build
```

The generated configuration binds SSH to host loopback on port 2222. To connect
directly from another machine, set `JAG_SSH_BIND` to the host's reachable interface
address and allow that port, or use an existing SSH jump host/tunnel.

Print the host fingerprint through your trusted host access:

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml exec agent \
  ssh-keygen -lf /var/lib/jag/ssh/ssh_host_ed25519_key.pub
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
`jag environment --docker` and the documented Compose overlay.
The Compose deployment includes a scoped seccomp profile so Codex can create its
nested bubblewrap sandbox. It retains the other Moby syscall filters and adds no
host capabilities. See [verification and sandbox details](docs/verification.md).

## Plain SSH and phones

Connect from Termius or another SSH client as `agent`, then run `jag connect` for
the workspace/running-session browser. No Jag installation is needed on the phone.
Set Termius's host Startup Command to `jag connect` to open it on connection.
The browser adapts to terminal size, shows readable project names and session
status, and supports single-key selection plus keyboard navigation and paging.
See [phone setup, keys and direct workspace commands](docs/plain-ssh.md).

## Register and connect

```sh
jag remote add work --host example.org --port 2222 --user agent --identity /path/to/key
jag remote list
jag remote use work
jag doctor
jag auth codex
```

Existing SSH aliases and jump hosts work: `--host my-agent-alias`. `JAG_SSH_CONFIG`
selects an alternative OpenSSH configuration file. The default configuration is
`jag/config.json` in the OS user configuration directory; `JAG_CONFIG` overrides it.
If you need a new key, run `jag keygen` first, add the displayed public key on the
Compose host, then register using its private-key path. Passphrase setup is
handled by OpenSSH. Concurrent configuration updates fail rather than overwrite
each other's changes; retry the command. An interrupted config update may leave
a `config.json.lock` directory; remove it only after confirming no Jag writer runs.

From a repository:

```sh
jag                 # Offer initial clone, subsequently reopen
jag --clone         # Skip initial clone confirmation
jag resume          # Attach if running, otherwise plain codex resume
jag --remote work   # Select and remember this remote for this checkout
jag new --worktree  # Additional session in a separate Git worktree
```

Cloning reproduces the local branch and commit, including unpublished history via
a Git bundle. The remote needs access to origin. Local working edits are not part
of clone. Launching from a repository subdirectory preserves that relative path.

From another folder:

```sh
jag --copy          # Initial file copy, then open Codex
jag                 # Reopen the associated copied workspace
jag sessions        # Remote workspace and running-terminal browser
```

Detach using **Ctrl-b d**. Disconnecting SSH also leaves the tmux terminal running.
Opening it again attaches rather than starting a duplicate. Several matches show
a selector. Multiple clients can share the terminal; `--takeover` detaches others.
`jag new` explicitly starts another session in the same files, after a notice.

## Multiple Node and Go versions

Projects can select independent toolchains in one container using `mise.toml`:

```toml
[tools]
node = "20.20.0"
go = "1.26.0"
```

Another project can select different versions and run concurrently. Jag installs
missing versions before starting Codex, with a persistent shared installation
cache. `.nvmrc`, `.node-version`, `.go-version` and supported `go.mod`/`go.work`
directives also work. Codex's own launcher keeps its fixed image Node runtime.

Use `jag tools` to inspect a workspace's selected versions and `jag tools install`
to prepare them without opening Codex. [Project toolchain details](docs/project-toolchains.md)
cover SSH/editor behavior, version-file semantics and deployment.

## Transfer changes explicitly

```sh
jag sync push
jag sync pull
jag sync push --force  # Explicitly choose source on conflicts / override busy guard
jag sync push --delete # Propagate deletions of previously synchronized files
jag sync push --include-ignored # Explicitly include Git-ignored files
```

Reopening never automatically synchronizes or switches branches. Transfer tracks
content hashes and previous endpoint states. Conflicts abort before writes;
destination-only changes and files are preserved. Transfers into an active
Jag-managed workspace require an explicit override. Editor writes are not locked:
avoid editing the destination during transfer.

Current protocol limits: 64 MiB of file content per snapshot, held in memory;
regular files and internal relative symlinks; no empty
directory replication, Git index transfer, submodule transfer, or Git LFS object
management. A failed I/O operation can leave earlier individual file updates in
place; each regular file replacement is atomic, the whole batch is not.

Git repositories use `git ls-files --cached --others --exclude-standard`.
`.jagignore` additionally supports `*`, `?`, `**`, root anchoring, directory
prefixes, comments, and ordered `!` exceptions. It is a documented subset of
gitignore syntax (no bracket patterns or escaped whitespace). In non-Git folders,
all regular files are eligible unless excluded; exclude credentials and generated
directories explicitly. `.git` metadata is never copied.

## Editors

```sh
jag config set editor code
jag config set editor nvim --remote work
jag edit
jag edit --editor vim
```

VS Code needs its local `code` command and Remote SSH extension. Jag generates
`~/.ssh/jag_config` and adds an Include to `~/.ssh/config`; existing configuration
is retained. Vim/Neovim must be selected in the environment build and run remotely.
Import a configuration directory with conflict protection:

```sh
jag editor import nvim --source /path/to/nvim-config --remote work
jag editor import vim --source /path/to/vim-config --remote work
```

Neovim configuration goes to `~/.config/nvim`; Vim configuration goes to `~/.vim`
(use a `vimrc` file there). Plugins and their dependencies are not installed
automatically. Missing editor binaries produce host setup instructions.

Other local editors can use an explicit argument array; no shell interpolation:

```sh
jag config set editor-command custom '["my-editor","--ssh","{ssh_alias}","{remote_path}"]'
jag config set editor custom
```

## Port forwarding

```sh
jag forward 3000
jag reverse 5432
jag forward 8080 --target 127.0.0.1:3000
```

Forward listens on the laptop and reaches the container. Reverse listens inside
the container and reaches the laptop. Listeners use loopback unless `--bind` is
explicitly supplied. Ctrl-C ends the tunnel, without stopping Codex sessions.

## Optional tools and provider tokens

```sh
jag environment --codex-version 0.166.0 --go-version 1.27.3 \
  --node-version 22.22.0 --vim --nvim --glab-version 1.126.0 \
  --tea-version 0.16.0 --out deploy/.env
```

Existing output files are never overwritten. Host owners update the saved file
and rebuild. Application/tool versions are recorded; Debian package repositories
are not snapshot-pinned, so builds are not byte-for-byte reproducible.

Optional automatic Codex updates run on the Docker host:

```sh
jag host update --project jag-personal --compose deploy/compose.yaml \
  --env-file deploy/.env --check
```

Omit `--check` to apply an available stable update. The updater builds and tests a
candidate, defers while terminals or SSH connections are open, blocks new Jag
operations during replacement, and records the successful exact version in the
env file. [Enable the optional hourly timer](docs/automatic-updates.md) to automate
this. No timer is enabled by default.

Configure tokens explicitly through stdin, not command arguments:

```sh
jag auth gitlab --host gitlab.example.org --token-stdin < /secure/gitlab-token
jag auth gitea --url https://gitea.example.org --token-stdin < /secure/gitea-token
```

Alternatively set `GITLAB_TOKEN_FILE` / `GITEA_TOKEN_FILE` on the Compose host to
absolute token-file paths, and set `GITLAB_HOST` / `GITEA_URL`. Startup configures
the selected provider CLI and HTTPS Git credential helper. Install that CLI in
the image first. Tokens are stored by the provider in the persistent agent home;
they are not build arguments or image-layer inputs. Repeated startup with the
same token is skipped. To rotate Tea's token, remove its managed `jag` login first.
Direct token setup needs real-provider validation; automated tests use no tokens.

## Status and verification

Native conversation migration is **unavailable**, with an explicit error and no
source changes. See [migration findings](docs/migration.md). No summary fallback
is substituted automatically.

```sh
go test -race ./...
go vet ./...
docker build -f deploy/Dockerfile --build-arg CODEX_VERSION=0.166.0 -t jag-agent:validation .
python3 scripts/e2e.py
# Also exercise editor configuration import with the optional-tool image:
# JAG_TEST_IMAGE=jag-agent:tools-validation python3 scripts/e2e.py
```

The transport smoke test uses temporary keys and a fake Codex process. For the
actual Compose deployment, real Codex UI, disconnect/reconnect, sandbox execution
and volume persistence, run `python3 scripts/real_codex_e2e.py` after building
`jag-agent:real-validation`. It needs no credentials or inference. An optional
`--live-auth PATH` explicitly enables a separate live turn.

One authorized live turn has also been verified; see the [verification report](docs/verification.md)
for the exact scope and the sandbox issue it exposed. [Agreed design](docs/design.md)
distinguishes the target behavior from remaining implementation work.
