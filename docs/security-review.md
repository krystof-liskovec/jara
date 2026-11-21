# Security review — 2026-10-30

Status: the confirmed startup privilege escalation is fixed and reproduced as a
passing regression test against the rebuilt container. Workspace transfers now
use Go's `os.Root` APIs and reject static symlink parents and links into `.git`.
This remains a development prototype, not an independently audited isolation
boundary. The review and fixes cover the findings below; they do not establish
security against hostile workloads or a compromised Docker host.

## Resolved: sshhole password authentication bypass

OpenSSH 10.6p2 addresses the password bypass reported for Linux systems using
musl without PAM. Jara's SSH server runs in the Debian `bookworm-slim` service
image with glibc, and its SSH configuration permits public key authentication
only. `AuthenticationMethods publickey` and `PasswordAuthentication no` are
checked against OpenSSH's effective configuration before the server starts, so
the password path remains disabled if the base image or libc changes. The
optional rootless Docker sidecar uses Alpine but does not run an SSH server; the
published port reaches the Debian service container's `sshd`.

Sources: [OpenSSH 10.6p2 release notes](https://www.openssh.org/releasenotes.html#10.6p2)
and the [researcher's write-up](https://sshhole.com/).

## Auto-mode approval timeout

Auto mode is on by default. In auto mode, an approval request that the agent's
reviewer passes to the user is approved automatically after 3 minutes without
an answer, including commands the reviewer declined to approve on its own.
Unattended sessions therefore keep running actions nobody looked at. Use
`/auto off` (or Shift+Tab) when every escalated action needs an explicit
`/approve`.

## Configurable session authority

Jara adds no Codex permission flags by default. The host can set
`JARA_CODEX_ARGS`, save defaults with `jara config codex-args`, or a client can
forward arguments to a new session. Explicitly passing
`--dangerously-bypass-approvals-and-sandbox` disables Codex approvals and
its workspace sandbox. Codex runs as the unprivileged `agent` account inside Docker,
but can read and modify every file available to that account, including other
workspaces and credentials in its home, and can use the container network. The
container is the remaining execution boundary; it does not isolate workspaces or
credentials from Codex. Use only with trusted users and repositories. OpenAI's
[Codex CLI reference](https://learn.chatgpt.com/docs/cli/reference) describes this
flag as suitable only inside an externally hardened environment.

## Resolved: agent-controlled paths in root startup

Before the fix, root startup copied authorized keys and changed ownership under
the agent-writable `/home/agent/.ssh` path. Two disposable Docker tests confirmed
that an agent-created symlink could redirect those operations, change ownership
of a root executable directory, and then cause a replacement startup script to
run as UID 0 after another restart. The payload only wrote a container-local UID
marker. No unauthenticated attack or host escape was demonstrated.

The fix stores keys under root-owned `/etc/ssh/authorized_keys` and atomically
replaces the per-user key file there. Startup no longer creates, repairs, copies
through, or changes ownership under `.ssh`. It creates `/tmp/jara-cache` through a
root-owned temporary directory and rejects symlinks or unexpected owners on
later starts instead of running privileged `chmod` or `chown` through that path.

`python3 scripts/security_e2e.py` rebuilds no image itself; build
`jara-agent:security-validation` first. The script confirms that a `.ssh` symlink
cannot change executable ownership, and that a cache symlink makes startup exit
without changing the root-owned target. It uses a generated key and disposable
container, with no real credentials or model calls.

## High-risk design: optional privileged Docker sidecar

`deploy/compose.docker.yaml` uses `privileged: true` for the outer rootless DinD
container. The normal agent remains unprivileged, there is no host Docker socket,
and nested daemons have independent storage. Those properties do not establish
hostile-workload isolation. Docker documents that privileged mode is required for
this rootless DinD setup:
[Docker rootless DinD](https://docs.docker.com/engine/security/rootless/tips/#rootless-docker-in-docker).

The nested API can mount the shared agent home and change files outside the
current Codex workspace. Docker-enabled Codex launches also enable workspace
sandbox networking. Do not treat the Codex filesystem sandbox as constraining
Docker-side effects. Use a separate VM per trust boundary for untrusted workloads;
review resource limits, network isolation and the privileged bootstrap separately.
No host escape was attempted or established in this assessment.

## Workspace transfer boundary

`internal/jara/sync.go` now validates transfer paths, streams hashes for sync
manifests, bounds changed-file payloads and ignore-file reads, and performs reads
and mutations through `os.Root`. Existing symlink
parents are rejected; transferred symbolic links that resolve into `.git` are
rejected. This prevents ordinary path traversal outside the workspace and blocks
the tested static symlink paths. Regression coverage includes an outside-root
sentinel and a `.git/config` sentinel.

There are still limits. Go documents that `os.Root` follows symbolic links that
stay within its root and does not prevent traversal across mount points. A process
with the same account can also mutate the workspace concurrently with Jara, so the
current preflight checks do not make a hostile same-UID process safe to share.
Treat the agent account and selected workspace as one trust boundary. Avoid
mounting untrusted filesystems inside workspaces. A stronger boundary requires
separate OS users or VMs, plus platform-specific no-follow and mount controls.
See [Go's `os.Root` behavior](https://go.dev/blog/osroot).

## Shared authority and exposure

- All SSH keys for one environment enter the same `agent` account. Sessions,
  workspace files, provider credentials and Codex login state share that account.
  There is no per-repository user/secret boundary. File mode 0600 does not protect
  credentials from another process running as the same user.
- TCP and Unix-socket forwarding are enabled. `GatewayPorts no` is enforced by
  default, so remote reverse listeners stay on loopback even if a client requests
  a wider bind. The host owner can opt in to `clientspecified` with
  `JARA_GATEWAY_PORTS=clientspecified`; use that only for trusted users.
  See [OpenSSH GatewayPorts](https://man.openbsd.org/sshd_config#GatewayPorts).
- Devices that run `jara device allow` are reachable through every host they
  link to: anyone, or any agent, that can log in as `agent` on such a host can
  start agents on those devices, as their user and outside any container. Their
  consent is local and revocable (`jara device deny`); keep hosts on a private
  network. See [network of devices](network.md).
- Devices that joined with a code log in with keys the host lists through
  `jara _peer-keys` (or Jara's managed `authorized_keys` block). That list
  lives in the `agent` account's data directory, so, unlike your own keys in
  the root-owned file, an agent on the host can add a login key there; the
  standby-peer keys it also lists stay forced to their restricted command.
  Remove unknown entries with `jara network revoke`.
- Publishing SSH alone is not a constrained agent execution service:
  authorized keys grant ordinary shell/SFTP and network access by design.
- The seccomp profile relaxes namespace/mount-related syscall filters for Codex's
  nested sandbox. Kernel capability checks remain, but the host kernel and Docker
  runtime are still part of the security boundary.
- The agent container sets `apparmor=unconfined` so bubblewrap can change mount
  propagation. This removes Docker's AppArmor layer for that container; seccomp
  and Docker's capability restrictions remain. Treat the host kernel and runtime
  as part of the security boundary, and do not use this configuration for hostile
  workloads.

## Checks and existing protections

Observed protections include key-only SSH, disabled root/password SSH login,
SSH host-key checking in the client, loopback-only host publication by default,
no host Docker socket mount, an unprivileged agent account, token input outside
command arguments, bounded transfers, rooted filesystem operations, explicit
destructive sync flags and terminal-control sanitization in the browser.

The Go vulnerability scan reported no reachable Go package vulnerabilities for
the scanned tree. This does not cover application logic, container OS packages,
Node/Codex, mise-installed toolchains, provider CLIs or the host Docker/kernel.
A complete image vulnerability/SBOM assessment and independent penetration test
have not been performed.

## Before wider deployment

1. Keep the optional privileged Docker sidecar limited to trusted workloads.
2. Decide and enforce the intended forwarding, credential and workload boundaries.
3. Scan built images and host components; use scoped credentials and independent
   backups. Pin artifacts/digests and define security update ownership.
4. Obtain an independent review if this will host untrusted users or workloads.

Until those boundaries are addressed, avoid sensitive credentials and untrusted
workloads in this prototype.
