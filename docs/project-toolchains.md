# Concurrent project toolchains

Jara supports multiple installed Node and Go versions in the same container.
Each project selects its tools independently; opening one project never runs a
global `mise use` or changes another project's selection.

The container includes mise 2026.10.24. No mise installation is required on the
laptop. Installed versions and download caches live in the persistent agent home,
so recreating or updating the container does not discard them.
Go compiler/module caches and npm's package cache default to `/tmp/jara-cache`.
They remain writable for sandboxed Codex commands. Jara-managed Codex
sessions use Codex permissions selected by configuration and explicit startup
arguments; see the [security review](security-review.md).
These build and package caches are regenerated after container recreation;
installed Node/Go versions remain persistent.

## Configure a project

Commit a `mise.toml` (or `.mise.toml`) in the repository:

```toml
[tools]
node = "20.20.0"
go = "1.26.0"
```

Another project can independently use:

```toml
[tools]
node = "22.22.0"
go = "1.27.3"
```

Jara installs missing tools before creating a Codex terminal. It then launches the
actual Codex executable through `mise exec`, with that project's resolved PATH
and tool environment. Both sessions can run at once. Existing running sessions
are reattached without replacing their environment.

Use exact versions or a committed `mise.lock` for reproducibility. Floating
requests such as `node = "22"` or `latest` follow mise's resolution rules; Jara
does not rewrite repository configuration or automatically generate a lockfile.

Version configuration must reach the remote workspace. Commit it before using
`jara --clone`, use `jara --copy` for an initial working-directory copy, or transfer
it to an existing workspace with the explicit sync commands.

## Existing version files

Node and Go idiomatic version readers are enabled. These include `.nvmrc`,
`.node-version`, `.go-version`, and supported exact-version directives in `go.mod`
and `go.work`, as well as mise's native configuration and `.tool-versions`.

For example:

```text
# .nvmrc
22.22.0
```

```go
// go.mod
module example.org/service

go 1.27.3
```

Go itself interprets module/toolchain directives as minimum requirements. Mise's
reader can select an exact patch release from them; a bare `go 1.27` is not an
exact release pin. Use a patch-level directive or explicit mise configuration.
Jara defaults `GOTOOLCHAIN=local` so Go uses the selected installed toolchain and
reports a mismatch instead of silently downloading a different one. A deliberate
project environment override can change that behavior.

Full mise configuration retains native mise behavior, including its environment,
trust and hook semantics. For straightforward version selection, plain `[tools]`
entries are sufficient. Jara does not enable blanket trusted paths or globally
answer yes to mise prompts.

## Inspect and install

From an associated local workspace:

```sh
jara tools           # Show selected remote project versions
jara tools current
jara tools install   # Install missing configured versions without starting Codex
```

These commands accept the normal `--remote` and `--source` workspace options.
An invalid configuration or failed installation produces a visible setup error;
Jara does not create a replacement terminal or silently change the requested pin.

After changing versions, close/restart the relevant Codex or editor process so
its inherited environment is refreshed. The process environment resolved at
startup stays with that process. For a different nested project, start Jara from
that subdirectory or explicitly use `mise exec` there.

## SSH and editors

SSH environments and login shells include mise's cwd-aware shims on PATH. A
command such as `cd /path/to/project && node --version` uses that project's
selection. First use can install a missing configured version. Projects without
a matching selection fall back to available image defaults.

`jara edit` prepares the selected project tools before opening the editor. Vim and
Neovim started through Jara receive the project's environment. VS Code Remote SSH
can discover the shims through SSH's PATH and invoke tools with the project's
working directory. Individual extensions that cache absolute executable paths
may need workspace-level settings or a restart. Native VS Code GUI behavior was
not tested by the automated suite.

Codex's npm launcher explicitly uses `/usr/local/bin/node`, followed by its native
Codex binary. The project's Node selection cannot replace this infrastructure
runtime. `NODE_VERSION` in the deployment env file selects that fixed runtime;
`GO_VERSION` optionally supplies an image-wide Go fallback. Project pins take
precedence over these defaults. `MISE_VERSION` selects the manager release and is
not changed by automatic Codex updates.

## Deployment and verification

Rebuild the container once from the current source. Thereafter, adding another
project Node/Go version does not require an image rebuild or another container.

```sh
docker compose -p jara-personal --env-file deploy/.env \
  -f deploy/compose.yaml up -d --build
```

The integration test uses two real Codex processes without authentication or model
calls, executes Node and Go commands over SSH, compiles a Go program, checks login
shell behavior, and verifies toolchain persistence after container recreation:

```sh
docker build -f deploy/Dockerfile --build-arg CODEX_VERSION=0.166.0 \
  -t jara-agent:toolchains-validation .
python3 scripts/toolchains_e2e.py
```

To also check actual Neovim processes and image-default Go fallback:

```sh
docker build -f deploy/Dockerfile --build-arg CODEX_VERSION=0.166.0 \
  --build-arg GO_VERSION=1.27.3 --build-arg 'EDITOR_PACKAGES=vim neovim' \
  -t jara-agent:toolchains-editors-validation .
JARA_TEST_IMAGE=jara-agent:toolchains-editors-validation python3 scripts/toolchains_e2e.py
```

With Codex 0.166.0, the sandbox places an empty protected `.git` directory in a
non-Git workspace. Go can mistake it for VCS metadata when stamping a build. In a
copied non-Git folder, use `go build -buildvcs=false` if this occurs. Normal Git
repositories can use plain `go build`. Jara does not globally disable VCS stamping
or initialize Git repositories automatically. The integration test covers both
forms inside the actual Codex sandbox.

Primary documentation: [mise Docker images](https://mise.jdx.dev/mise-cookbook/docker.html),
[Node version files](https://mise.jdx.dev/lang/node.html),
[Go version files](https://mise.jdx.dev/lang/go.html),
[process environments](https://mise.jdx.dev/cli/exec.html),
[shims](https://mise.jdx.dev/dev-tools/shims.html).
