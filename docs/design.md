# Agreed design

Confirmed by the project owner on 2026-10-30 after questions Q1–Q30.

## Architecture

Use standard OpenSSH and Go; do not build a custom SSH server. The host owner
deploys Linux agent containers with native Docker commands or Docker Compose. One
owner per container; separate containers, credentials, persistent home and host
keys per environment. No cloud provisioning or local Docker dependency in the
client.

The Go client supports Linux, macOS and Windows. It registers named connections,
validates them and remembers a default. Guided setup can deploy the bundled Jara
source to an existing Docker host over SSH using native Docker commands. It does
not install Docker or manage host firewall rules. Every local checkout also
remembers its last selected remote and its association on each remote. An
explicit remote argument updates that remembered choice. Local worktrees are
distinct checkouts.

Ordinary SSH execution, file transfers and forwarding remain available. Jara
invokes its remote entry point explicitly rather than forcing all SSH commands
through a menu. This permits VS Code Remote SSH and other editors to operate.

## Launch and session behavior

- Known workspace: attach to one running session, select if several exist, or
  start Codex if none exists.
- `resume`: attach if running; otherwise run plain `codex resume`, without
  `--last`. Codex owns saved-conversation selection.
- The browser opens on projects ordered by saved conversation activity. Each
  project opens a conversation list with agent labels, prompts and live counts.
  Terminal management is a separate action. A native Codex picker remains as a
  fallback if the local history API is unavailable.
- New repository: offer initial clone. `--clone` skips that prompt. Before
  starting a session, offer to push unsynced local file changes if present.
- `--copy`: initially copy the selected source directory. Subsequent use opens
  the association without an implicit transfer.
- No repository or copied-workspace context: open the remote workspace and
  running-session browser.
- Preserve the relative working directory inside a Git repository; associate
  the repository root. Explicit copy selects the current/source directory.
- Opening never silently replaces files or changes the remote branch. Report
  local/remote branch differences.
- A second client shares the running terminal, with a notice. Explicit takeover
  detaches other clients.
- Creating an additional session is explicit. Sharing files produces a notice;
  a worktree option provides a separate Git worktree and branch. Copied folders
  need a separate copy for file isolation.

tmux is the terminal supervisor. Before creating a managed session, Jara queries
the connected terminal for its background color and passes that value into tmux
and a small PTY relay. The relay answers Codex's color queries and forwards
keyboard input, output and resize events. Detach and network loss leave processes
running. Container stop/replacement and host reboot do not preserve processes;
durable workspaces, Codex history and Jara metadata remain available afterwards.
Attached clients, running terminals and saved conversations are separate concepts.
Do not claim reliable agent-busy status solely from a running terminal.

## Repository and transfer behavior

Clone recreates origin, branch and exact local commit, including unpublished
history using a Git bundle. Copy transfers working files, edits and eligible
untracked files, independently of Git history. Exclusions use Git ignore rules
where available plus `.jaraignore`; ignored-file inclusion must be explicit.

Push and pull are separate, explicit commands. Record previous successful
transfer state and content hashes. Detect conflicting edits and require an
explicit source choice. Preserve destination-only files by default. Propagating
deletions requires an explicit option. Refuse transfer into an environment with
a running Jara-managed agent unless explicitly overridden.

Sync compares file content, symlink targets, and executable status. Ordinary
read/write permission differences (such as 0600 versus 0644 after editing in a
container) do not constitute different workspace content. The original modes
remain on transfer manifests and expected-value checks, so a concurrent change
is still detected before writing. Before prompting to push, compare pending
local changes with the remote manifest and acknowledge converged files in the
sync baseline. A shared Git commit alone cannot prove convergence: uncommitted
edits and untracked files still need to be checked.

## Authentication, tools and editor configuration

Host configuration authorizes public SSH keys. Private keys remain on clients;
existing identities are reusable, and generating a key is an optional guided
setup capability. Verify first-connection fingerprints and preserve host keys.
Never silently accept a changed key.

Guide remote Codex authentication; import credentials only when explicitly
requested. Git credentials can be imported for the current Git instance over SSH
stdin and stored in the persistent agent home for clone and push across its
repositories. GitLab and Gitea are optional, including self-hosted instances. Keep tokens outside
image layers, command arguments and diagnostic logs. Explicit mounted token files
can configure provider tools on startup.

The host owns a recorded, rebuildable environment definition with selected Node,
Go, provider CLIs and editors. The client diagnoses missing tools and explains
host updates. Guided setup can transfer its embedded deployment bundle and run
Docker over SSH when Docker is already available to the host account. Guided
uninstall stops that deployment and removes its staged files while preserving
Docker volumes and local workspace links by default; volume deletion is explicit.

Project Node/Go versions can coexist inside that environment. Mise installs them
in the persistent agent home; each Codex process receives its project's selected
tool environment. SSH shims also select tools by working directory. Codex's
launcher retains its fixed infrastructure Node runtime. See
[project toolchains](project-toolchains.md).

Editor preference is local, overridable per remote and invocation. VS Code runs
locally with Remote SSH; Vim/Neovim runs in the environment. Dotfile import is
explicit and must report missing dependencies. Advanced custom-editor argument
lists use named adapters with literal argument arrays.

## Networking

Forwarding supports both laptop → container and container → laptop services.
Listeners default to loopback on their receiving side. The SSH server enforces
loopback for reverse listeners unless the host owner sets
`JARA_GATEWAY_PORTS=clientspecified`; public reverse binds then also require an
explicit bind address. Tunnels last only while the local forwarding connection
exists, independently of persistent agent terminals.

## Conversation migration

Native resumable conversation transfer is a requirement. Require an idle source,
retain the original, validate the destination, and report missing capabilities
or unsupported fidelity. Summary continuation is a separate opt-in fallback.

The verified Codex 0.166.0 public interface does not currently provide a supported
native CLI import. See [evidence and acceptance criteria](migration.md). This is
an upstream feasibility blocker, not a completed implementation.

## Optional nested Docker

The agent bundles Docker CLI, Compose and Buildx. An opt-in Compose overlay adds
one rootless daemon and persistent image/volume store per environment. A private
Unix socket and the agent home are shared with the sidecar; the host Docker socket
is never mounted. The agent joins the daemon's network namespace, keeping nested
published services reachable through SSH forwarding while permitting independent
agent recreation. The outer sidecar requires privileged mode, and Docker API access
can alter the shared agent home outside any one workspace. Jara-managed Codex
sessions use configured Codex permissions; the host or client can explicitly
opt into full access through additional Codex arguments. See
[deployment, networking and isolation scope](docker.md).

## Current implementation boundaries

The README documents operational commands and transfer limits. Further work is
required for native migration and comprehensive editor plugin dependency checks.
Windows and macOS need native end-to-end validation. Provider token setup requires validation
against real instances; automated testing must not use personal tokens or API
calls. Minimal Linux container testing uses a fake Codex process so terminal and
transport behavior can be verified independently from inference/authentication.

## Primary references

- [OpenSSH command routing](https://man.openbsd.org/sshd_config#ForceCommand)
- [SSH channel types](https://www.rfc-editor.org/rfc/rfc4254#section-6.5)
- [tmux persistence and attachment](https://man.openbsd.org/tmux)
- [Codex CLI reference](https://learn.chatgpt.com/docs/cli/reference)
- [Codex headless authentication](https://learn.chatgpt.com/docs/auth)
- [Git bundles](https://git-scm.com/docs/git-bundle)
- [VS Code Remote SSH](https://code.visualstudio.com/docs/remote/ssh)
- [GitLab token stdin and credential helper](https://docs.gitlab.com/cli/auth/login/)
