# Container verification

Verified on 2026-10-30 with Docker Engine 29.9.2, Linux amd64, Go 1.27.3,
OpenSSH, tmux 3.3a in the container, and the real Codex CLI 0.166.0.

## What was exercised

The first transport suite (`scripts/e2e.py`) uses a fake Codex process so file
transfer, terminal inventory, editors, remote selection and networking can be
tested deterministically without credentials or inference. It also verifies
that a Codex pane receives a UTF-8 locale and its attached tmux client detects
UTF-8. `scripts/codex_args_e2e.py` checks literal environment and client argument
forwarding for normal and resumed sessions, including plain SSH connections.

`scripts/clipboard_e2e.py` exercises a local fixture clipboard through Jara's PTY
proxy and SSH upload into the real Codex composer. It checks `[Image #1]`, exact
transferred PNG bytes, preservation of an unsubmitted draft, terminal resizing,
and tmux detach. It also checks that the Codex composer has a background color
inside tmux and verifies the terminal's detected RGB value reaches its pane and
Codex process. It uses a dummy API key and an unreachable container-local API
endpoint, with no personal credentials or model calls. It does not establish
native macOS clipboard behavior or Windows clipboard forwarding.

The second suite (`scripts/real_codex_e2e.py`) uses the actual Compose deployment,
named persistent volumes, OpenSSH, Jara, tmux and the installed **real Codex binary**.
It does not replace Codex or copy a different Jara binary into the image.

Confirmed with the real binary:

- Fresh-container sign-in UI is visible through `jara --copy`.
- Abruptly killing the local SSH process leaves the same remote terminal alive.
- Reopening attaches to that same session, rather than creating a duplicate.
- Ctrl-b d detaches the interactive client.
- A real Codex sandbox can write in the workspace and cannot write outside it.
- Compose container recreation preserves workspace data and SSH host identity.

With explicit authorization, a temporary copy of the existing ChatGPT login was
used for **one live model turn**. Codex created `jara-live-check.txt` with exactly
`jara-live-ok\n` and replied `JARA_LIVE_OK`. Reattachment showed the completed
conversation. After Compose recreation, plain `jara resume` displayed that saved
conversation in Codex's picker. The temporary login, container and volumes were
removed. No credential values are included in this report or the test output.

The live run occurred before the sandbox fix below. Its initial sandboxed shell
attempt failed, then the requested fixture command succeeded after explicit
one-time approval. The corrected sandbox was subsequently tested directly with
the real `codex sandbox` command, without making another model call.

The first live script run's final assertion expected a filename in the picker
title. Codex generated the title “Create JARA live check file” instead; the captured
picker visibly contained the saved conversation. The test now checks picker
inventory and reopens the selected transcript, rather than assuming its title.

## Issue found and fixed: nested sandbox namespaces

Docker's default seccomp profile blocked the namespace creation needed by Codex's
bubblewrap sandbox. This was a real deployment defect that the fake-process
transport suite could not detect.

The minimal reproducer failed with `Operation not permitted`:

```sh
docker run --rm --user agent --entrypoint /bin/sh jara-agent:real-validation \
  -c 'unshare --user --map-root-user true'
```

Testing one restriction at a time isolated seccomp: removing only AppArmor's
profile did not help; changing seccomp did. The host already allowed unprivileged
user namespaces. No host sysctls were changed.

The deployment now installs bubblewrap and uses a pinned copy of Moby's default
seccomp profile with six additional allowed calls: `clone`, `unshare`, `setns`,
`mount`, `umount2`, and `pivot_root`. These permit nested namespace setup while
retaining the remaining syscall filter and the container's capability boundary.
The deployment does not use privileged mode, `CAP_SYS_ADMIN`, host namespaces,
or `seccomp=unconfined`. Enabling these syscalls does expand the accessible kernel
surface; the profile is intended for these personal development environments.

See [profile provenance](../deploy/seccomp/SOURCE.md) and its Apache-2.0 license.
The profile was tested with this Docker Engine; hosts may have additional
AppArmor, user-namespace or runtime restrictions. `jara doctor` now executes a real
Codex sandbox probe and reports a setup failure when it cannot run.

Sources: [Docker seccomp documentation](https://docs.docker.com/engine/security/seccomp/),
[Moby profiles](https://github.com/moby/profiles),
[Codex approvals and security](https://learn.chatgpt.com/docs/agent-approvals-security).

## Repeat the tests

```sh
docker build -f deploy/Dockerfile --build-arg CODEX_VERSION=0.166.0 \
  -t jara-agent:real-validation .
python3 scripts/real_codex_e2e.py
JARA_TEST_IMAGE=jara-agent:real-validation python3 scripts/e2e.py
go test -race ./...
go vet ./...
```

To explicitly authorize a separate live test and its model usage:

```sh
python3 scripts/real_codex_e2e.py --live-auth "$HOME/.codex/auth.json"
```

The script copies only the specified login cache, never prints it, and removes
its Compose containers and volumes in cleanup. It creates a fresh fixture
workspace, never uploads the user's working repository, and never modifies the
source login cache. Without `--live-auth`, no personal credentials or inference
are used.

## Still outside this verification

Live GitLab/Gitea authentication, VS Code GUI interaction, native Windows/macOS
terminal behavior and native conversation migration were not verified. The
migration limitation is documented separately in [migration.md](migration.md).

## Automatic updater

`python3 scripts/update_e2e.py` exercised a real Docker deployment upgrading from
Codex 0.165.0 to 0.166.0, without credentials or inference:

- A detached tmux session deferred the update without replacing its container.
- An open raw SSH connection also deferred replacement.
- The candidate image was built and its actual Codex sandbox tested in isolation.
- The successful replacement preserved persistent files and the SSH host key.
- The env file recorded 0.166.0 only after successful deployment checks.
- An active maintenance lease refused a new workspace mutation.
- A deliberately failing candidate health check restored the 0.165.0 image,
  preserved the previous pin and data, and released the maintenance gate.

Unit tests also cover a session opening during the build, failed candidate smoke
tests, concurrent env-file changes, the workspace admission gate, and check-only
behavior. Systemd templates were parsed with `systemd-analyze verify` against a
temporary executable path, without installing or enabling a scheduler here.

See [automatic update setup](automatic-updates.md). The timer is opt-in.

## Concurrent project toolchains

`python3 scripts/toolchains_e2e.py` passed against the actual Compose image with
mise 2026.10.24 and two real Codex processes, without credentials or model calls:

- Project A: Node 20.20.0 and Go 1.26.0 from `mise.toml`.
- Project B: Node 22.22.0 and Go 1.27.3 from `.nvmrc` and `go.mod`.
- Both processes simultaneously inherited their own selected PATH and GOROOT.
- Codex's infrastructure executable stayed outside the project tool installations;
  its npm launcher uses the fixed `/usr/local/bin/node` runtime.
- Plain SSH and login-shell commands selected the correct project versions.
- Actual Neovim processes, launched through Jara's editor helper, invoked each
  project's selected Node and Go versions in the optional-editor image.
- Both Go programs compiled and ran over SSH and in the actual Codex sandbox.
- An invalid mise configuration failed before starting a third session.
- Both installed toolchains survived recreation and executed in mise offline mode.

This test exposed two non-version-selection details: Go/npm build caches need a
sandbox-writable location (now `/tmp/jara-cache`), and Codex 0.166.0 creates an empty
protected `.git` directory for a non-Git sandbox workspace. The latter can confuse
Go's VCS stamping; the test covers the explicit `-buildvcs=false` workaround for
copied non-Git folders and normal `go build` in actual Git repositories. Jara does
not globally disable stamping or automatically initialize repositories.

Installed toolchains and mise download caches persist in the agent home. Build
and package caches under `/tmp` are regenerated on container recreation. See
[project toolchain documentation](project-toolchains.md) for version precedence,
process environment lifetime and editor integration scope.

## Optional Docker-in-Docker

The optional rootless Docker deployment was exercised with Docker 29.9.2 using
`scripts/docker_e2e.py`, without credentials or model calls. Checks covered:

- Docker CLI, Compose and Buildx through ordinary SSH and login shells.
- A separate rootless engine, with the agent container remaining unprivileged.
- A real Buildx image build and nested container run inside Codex's actual
  workspace-write sandbox, including a mode-600 file in a mode-700 workspace,
  bind-mount writes, and refusal of direct shell writes outside the workspace.
- A nested Compose service, named volume, localhost publication and SSH forwarding.
- Recreating only the agent while retaining the daemon identity, running service,
  nested volume data and SSH host identity.
- Jara diagnostics and the updater's rejection of a missing Docker overlay;
  an update check using the overlay succeeded. Unit tests cover retaining the
  overlay for replacement commands and refusing edits during a build.
- Independent daemon identities and image stores in two Compose projects.

The tests found that OpenSSH honors only the first `SetEnv` directive; Jara now
emits all session variables together. They also established that Codex 0.166.0
requires workspace sandbox networking for Docker Unix-socket access when running
direct sandboxed commands. Jara-managed interactive sessions use permissions
selected by Codex configuration and explicit arguments, as described in the
[security review](security-review.md). An
explicit user override disabling networking was separately verified to block
socket access for sandboxed commands.

The regular transport integration suite also passed against this agent image,
covering copy/clone, sync, sessions, editors, multiple remotes and both forwarding
directions. See [Docker setup and isolation scope](docker.md). The rootless sidecar
still requires a privileged outer container; these checks do not establish hostile
workload isolation or behavior on other host kernels/storage drivers.

## Plain SSH and phone entry point

`jara connect` exposes the environment's existing workspace/session browser directly
on the server. `scripts/e2e.py` passed menu selection, direct workspace attachment,
reuse without duplicate terminals, detach, plain resume, and ordinary shell
execution through standard SSH without a local Jara client process for those steps.

A fresh `jara-agent:phone-validation` image built from the updated source passed
`scripts/real_codex_e2e.py`. After an abrupt laptop SSH disconnect, standard
`ssh -tt ... jara connect WORKSPACE_ID` reattached to the same actual Codex terminal
and sign-in UI. No credentials were imported and no model calls were made. Real
sandbox boundary checks and Compose persistence checks also passed. Disposable
containers and volumes were removed. Native Termius/iOS/Android behavior remains
unverified on-device; see [connection instructions](plain-ssh.md).

## Responsive terminal browser

The responsive browser was tested on 2026-10-30 with a fresh
`jara-agent:responsive-validation` image. `scripts/menu_e2e.py` uses actual OpenSSH
PTYs and 35 browser entries to check:

- 32-, 40- and 80-column layouts, bounded rows and columns, pagination, arrow-key
  navigation and live terminal resize.
- Recovery after the terminal becomes too short to display the list.
- Restoration of the original terminal settings after `q` and Ctrl-C.
- Single-key creation of a session from a later page, starting the actual Codex
  sign-in UI at 40 columns, abrupt disconnect and successful reattachment.

Unit tests additionally cover 16–120 columns, 7–40 rows, all positions in a long
list, wide characters, combining marks, emoji grapheme clusters, terminal-control
sanitization, long workspace names, and keeping session numbers, shortcuts and
the selected workspace ID visible on a 32-column screen. The general transport
integration suite passed, including persistence of the copied folder's readable
name. Go race tests and vet passed; Windows amd64 and macOS arm64 clients compiled.

This optimizes Jara's browser. Codex owns its attached terminal interface, and
Termius/iOS/Android behavior still requires native-device validation. The tests
imported no credentials and made no model calls. Disposable test containers and
volumes were removed. Set `JARA_MENU_SNAPSHOT_DIR` when running the menu suite to
save plain-text captures of the actual narrow and wide SSH layouts.
