# Running the agent on your device (experimental)

`jara --local` runs the agent on the machine you are sitting at, against your
local checkout, with your local Codex and Claude Code CLIs and sign-in. The
remote host keeps the shared copy: after every turn it receives the checkout's
commits and edits and the conversation itself. Any device can watch the
running conversation or continue it, including the remote host.

This is an experimental branch feature. It needs a remote host running the same
Jara version (`jara deployment upgrade --remote NAME`), and a Unix client
(Linux or macOS) with `codex` on `PATH`. `claude` is used when installed.

## Model

Exactly one machine runs a workspace's agent at a time. The remote host keeps a
**runner lease** for each workspace: the device holding it, an epoch that
increases with every claim, and the latest **checkpoint**.

```text
 laptop (runs the agent)            remote host (shared copy)           phone / desktop
 ───────────────────────            ─────────────────────────           ───────────────
 local checkout ── per-turn sync ─▶ workspace checkout
 agent session  ── checkpoint ────▶ lease + conversation checkpoint
 chat session   ◀── ssh -R socket ─ runners/<key>-<epoch>.sock ◀────── jara connect
```

- **Claim.** `jara --local` claims the workspace. If another device holds it
  and is online, the remote host asks that device to release it (it saves a
  final checkpoint first). A device whose turn or scheduled work is still
  running refuses. The remote host's own idle session is stopped and
  checkpointed.
- **Catch up.** If the newest checkpoint came from elsewhere, this device sets
  aside conflicting local work (below), pulls the files with `jara sync pull`
  semantics, and installs the conversation.
- **Run.** The session runs as a background `jara _runner-daemon`, like the
  remote host's persistent session: `/detach` leaves it running, and `/quit`
  while idle ends it.
- **Checkpoint.** After every turn the device mirrors the checkout to the remote
  host (commits as commits, then uncommitted edits and deletions as files) and
  uploads the conversation.
- **Live view.** The device keeps a reverse SSH Unix-socket forward to the
  remote host. When the device holds the lease, `jara connect` on the remote
  host (a phone in Termius, for example) and `jara` on other machines attach to
  the device's live session instead of starting the agent on the remote host.
- **Hand over.** When the session ends on the device, it saves a final
  checkpoint and releases the lease. The next connection elsewhere continues
  from there. If the remote host continues, it installs the checkpoint into its
  own agent state first.

## Commands

```sh
jara --local                     # run here; continue the workspace's conversation
jara new --local                 # start a new conversation, running here
jara runner status               # which device runs this workspace, and is it online?
jara --local --take-over         # continue here although the running device is offline
jara connect --take-over ID      # on the remote host: continue there instead
```

`JARA_DEVICE` overrides the device name used in leases (the host name by
default).

## Offline devices and forced takeover

If the device running the agent is unreachable (lid closed, no network), other
devices refuse to start the conversation and report when that device was last
seen and when its last checkpoint was saved. `--take-over` continues from that
checkpoint. The epoch increases, so the offline device can no longer write
checkpoints. When it reconnects, its tunnel is refused and its session stops.

The next `jara --local` on that device sets aside anything the newer
checkpoint does not contain before catching up:

- Uncommitted work goes into a Git stash named `jara: local work set aside …`.
- Commits the remote copy lacks stay on a `jara/set-aside-<time>` branch, and
  the checkout is reset to the last commit it synchronized.
- In copied (non-Git) folders, changed files are copied to
  `~/.local/share/jara/set-aside/<id>/<time>/`.

Work done during a turn that never finished is only on the offline device.

## How conversations move

A checkpoint contains Jara's own record of the conversation (its transcript and
agent segments) plus each agent's native session file:

- Claude Code: `~/.claude/projects/<folder>/<session>.jsonl`
- Codex: the rollout under `~/.codex/sessions/`

The checkouts live at different paths on each machine, so Jara rewrites the
checkout path inside these files on import (`/home/agent/workspaces/w-…` ↔
`/Users/you/src/project`). Neither format is a documented interchange format,
which is why [native migration](migration.md) was previously refused. Here they
are used best-effort, with a fallback:

- If a native file is missing or cannot be installed, the conversation is
  marked to continue in a fresh agent session briefed on Jara's transcript.
- If Codex rejects the resumed thread, or a Claude Code session file is missing
  when the next turn starts, the same briefing fallback applies instead of
  failing.

The briefing keeps the exchange and the working tree, not the agent's internal
tool history. While a device runs the agent, Jara keeps a transcript even for
Codex-only conversations so this fallback always has something to replay.

## Limits of this experiment

- Only the workspace's current conversation moves. Other saved conversations
  stay on the machine that ran them.
- The remote host's checkout is a mirror while a device holds the lease. Edits
  made there directly, for example over plain SSH, are overwritten by the next
  checkpoint.
- Remote-host terminals (`--interface codex`) are not blocked while a device
  holds the workspace. The remote session browser does not list device-run
  sessions; connect by workspace instead.
- Each checkpoint opens several SSH connections, so connection multiplexing
  (`ControlMaster`) in your SSH config makes them faster.
- The device uses its own `PATH` and toolchains, not the remote host's mise
  environment, and its own agent configuration and credentials.
- The remote host's `sshd_config` now sets `StreamLocalBindUnlink yes` and
  `ClientAliveInterval 30`, so a dropped device tunnel is noticed and replaced.
  Older deployments still work, because the remote host removes stale tunnel
  sockets itself.
