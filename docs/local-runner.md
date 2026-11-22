# Device handoffs

Jara runs on its own: `jara` in a folder runs the agent on your machine, with
your local Codex and Claude Code CLIs and sign-in, and keeps the session and its
conversations on this device. No remote host is involved until you attach one.

Attaching the folder to a remote host (`jara --remote NAME`, once) adds device
handoffs. The agent keeps running on the machine you are sitting at, and the
host keeps the shared copy. After every turn, and every 20 seconds during long
turns, it receives the checkout's commits and edits, every conversation of the
workspace, and the work waiting in the session: queued and scheduled prompts and
an active `/crunch` or `/goalcrunch` task. Conversations the folder had before it
was attached are sent as soon as the session starts.

Any device can watch a running conversation live or continue it. When your
laptop goes to sleep in the middle of a turn, the remote host continues the
turn by itself. When the laptop wakes, its window follows the conversation and
takes it back as soon as the remote host is idle.

Handoffs need a remote host running the same Jara version (`jara deployment
upgrade --remote NAME`), and a Unix client (Linux or macOS) with `codex` on
`PATH`. `claude` is used when installed. Only the Jara interface takes part;
native Codex and Claude Code terminals (`--interface codex`) of an attached
folder keep running on the remote host.

## Commands

```sh
jara --remote work          # attach this folder to the remote host work
jara                        # run here when Codex is installed; otherwise on the host
jara --run-remote           # run this session on the remote host instead
JARA_RUN_REMOTE=1 jara      # the same, for every session in this shell
jara new                    # a new conversation, running here
jara runner status          # where the agent runs, online or not, last checkpoint
jara ps                     # running sessions: this device, remote hosts, other devices
jara attach 2               # join one where it runs, without moving it
jara daemon install         # run this device's sessions in a background service
jara --take-over            # take over although the other device still looks online
jara runner prefer laptop desktop  # who gets a conversation back first
jara remote order jellyfish backup # rank remote hosts; the first is the default
```

Inside a conversation, `/remote` moves it without leaving the window: it lists
this device and your remote hosts. Choosing the folder's host or this device
hands the workspace over and reconnects the window to the session there,
reloading the conversation. Choosing a host the folder is not attached to
opens Jara again with `--remote NAME`, which attaches it there (it may ask to
clone) while the agent keeps running here; choose it again to run there. A
conversation running on one host comes back to this device before it moves to
another, and a running turn is never moved.

`JARA_DEVICE` overrides the device name used in leases (the host name by
default). Several projects, or several Git worktrees of one project, are
independent workspaces: each runs wherever it was last opened. A folder's session
is keyed by its path, so attaching it keeps the queue, schedules and conversations
it had as a local-only folder.

## Model

Exactly one machine runs a workspace's agent at a time. The remote host keeps a
**runner lease** for each workspace: the device holding it, an epoch that
increases whenever the runner changes, and the latest **checkpoint**.

```text
 laptop (runs the agent)              remote host (shared copy)            phone / desktop
 ───────────────────────              ─────────────────────────            ───────────────
 checkout          ── sync ─────────▶ workspace checkout
 conversations     ── checkpoint ───▶ lease + every conversation
 link (heartbeats) ── ssh ──────────▶ jara _runner-link ─▶ lease renewed
 chat session      ◀── ssh -R ─────── runners/<key>-<epoch>.sock ◀──────── jara connect
```

- **Claim.** Opening a workspace claims it. An idle device holding it hands it
  over after a final checkpoint. A busy one finishes its turn first; the
  claimant watches meanwhile. The remote host's own session works the same
  way.
- **Catch up.** If the newest checkpoint came from another machine, the device
  pulls the files and installs every conversation that changed since it last
  saw them, along with queued and scheduled prompts and any `/crunch` task.
- **Run.** The session runs as a background `jara _runner-daemon`. `/detach`
  leaves it running; `/quit` while idle ends it and hands the workspace over.
- **Link.** One SSH connection per device carries a reverse Unix-socket forward
  to the live session and `jara _runner-link`, which renews the lease with a
  heartbeat every few seconds. Through it, `jara connect` on the remote host
  (a phone in Termius, for example) and other devices attach to the live
  session.

## Sleep, network loss and coming back

- **Going to sleep or shutting down with Linux and systemd.** Jara holds
  logind delay locks for suspend and shutdown (suspend alone where polkit
  refuses the shutdown lock). Just before either, if a turn is running or
  work is waiting (queued or scheduled prompts, a `/crunch` task), it saves a
  checkpoint (within about three seconds) and tells the remote host, which
  continues at once.
- **A session stopped while working** (SIGTERM at logout or shutdown, the
  [background daemon](#background-daemon) stopping, or its agent server
  exiting) does the same with up to 15 seconds for the checkpoint, instead of
  releasing the workspace and dropping the turn.
- **Disappearing without warning** (macOS sleep, network loss, a crash). When
  the heartbeats stop for 30 seconds, a watcher on the remote host takes over
  if a turn was running or work was waiting, and starts the session without a
  window. The interrupted turn continues
  with a note that the device went offline, from the last checkpoint: at most
  about 20 seconds of work is redone. Queued prompts follow it, and a `/crunch`
  task resumes from the saved conversation.
- **An idle device that sleeps** keeps the workspace. If you open the
  conversation elsewhere meanwhile, that machine takes over.
- **Waking up.** The device learns that another machine took over. Its session
  stops, and its open window reconnects. While the remote host is still busy,
  the window shows the remote session live. When the remote host is idle, it
  hands back and the window continues here, with the remote host's progress
  pulled in.

A window that reconnects by itself only takes the conversation back from the
remote host, never from another device you moved to. Open it again with `jara`
to move it explicitly.

Only remote hosts run agents unattended. A device runs one only for a session
started on it; the remote host never starts one there, and failover never goes
to another device. When windows on several devices wait for the remote host,
`jara runner prefer` decides who gets the conversation back: the most-preferred
waiting device, or after 20 seconds without it the next one.

Remote hosts are ranked (`jara remote order`; new ones rank last). The first is
the default and primary host.

## Standby hosts

`jara remote standby NAME` makes a lower-ranked remote a standby. After every
checkpoint, the primary forwards the workspace to its standbys in the
background, so your device sends each checkpoint only once:

```text
laptop ──checkpoint──▶ primary ──commits, files, conversations──▶ standby
```

- The standby keeps a mirror under the same workspace ID: commits as Git
  bundles, then files like `jara sync push`, then conversations as verified
  deltas of their session files. `jara runner status` shows how current each
  standby is.
- Your client connects the hosts. Each host has a peer key; a standby accepts
  the keys of the hosts ranked above it, and each such key may only run
  `jara _peer NAME`, which allows the workspace transfer and standby
  operations and nothing else. The primary pins each standby's SSH host key.
- In the container deployment sshd reads peer keys through
  `AuthorizedKeysCommand` (`jara _peer-keys`), which forces that command onto
  every key, so the agent user cannot add an unrestricted key. Other hosts get
  a managed block in `~/.ssh/authorized_keys` with the same options.
- Hosts reach a standby at the address your SSH configuration resolves its name
  to. When you reach it through an alias they cannot resolve, a jump host or a
  tunnel, set theirs: `jara remote standby backup --address backup.lan:2222`.
- While you run an agent in the workspace on a standby yourself, forwarding to
  it waits instead of overwriting the mirror.
- Standbys do not continue conversations by themselves yet.

Work a device did after its last checkpoint, before another machine replaced
it, is set aside before catching up:

- Uncommitted work goes into a Git stash named `jara: local work set aside …`.
- Commits the remote copy lacks stay on a `jara/set-aside-<time>` branch, and
  the checkout is reset to the last commit it synchronized.
- In copied (non-Git) folders, changed files are copied to
  `~/.local/share/jara/set-aside/<id>/<time>/`.

Edits you made yourself on a device that was not running the agent merge like
`jara sync pull`, which refuses conflicting changes.

## Branches

Both checkouts stay on one branch. When they differ, the one that switched
since the device's last sync leads:

- **The device leads** on its first sync, when you or its agent switched
  branch there, and always while it runs the agent. The remote host switches
  to the device's branch, creating it where the histories meet, and keeps its
  own uncommitted changes in a Git stash (`jara: work on … set aside …`).
- **The remote host leads** when only it switched, for example because its
  agent changed branch while your laptop slept. The device switches to that
  branch and brings its uncommitted changes along; Git refuses when they would
  conflict.

Commits on the branch left behind stay there.

## How conversations move

A checkpoint contains Jara's record of each changed conversation (its
transcript and agent segments) plus each agent's native session file:

- Claude Code: `~/.claude/projects/<folder>/<session>.jsonl`
- Codex: the rollout under `~/.codex/sessions/`

Checkouts live at different paths on each machine, so Jara rewrites the
checkout path inside these files on import (`/home/agent/workspaces/w-…` ↔
`/Users/you/src/project`). Neither format is a documented interchange format,
which is why [native migration](migration.md) was previously refused. Here they
are used best-effort, with a fallback:

- If a native file is missing or cannot be installed, the conversation
  continues in a fresh agent session briefed on Jara's transcript.
- The same happens when Codex rejects the resumed thread, or a Claude Code
  session file is missing when the next turn starts.

The briefing keeps the exchange and the working tree, not the agent's internal
tool history. Sessions taking part keep a transcript of every conversation,
including Codex-only ones, so the fallback always has something to replay.

## Limits

- While a device holds the workspace, the remote host's checkout is a mirror.
  Edits made there directly, for example over plain SSH, are overwritten by the
  next checkpoint.
- Handing back to a waking device ends other windows attached to the remote
  host's session, such as a phone; reconnect them to follow the conversation.
- Short requests to a remote share one SSH connection (OpenSSH
  `ControlMaster`, sockets in `$XDG_RUNTIME_DIR/jara-ssh` or
  `/tmp/jara-ssh-<uid>`), which closes after a minute without requests, so
  opening a session or checkpointing pays for authentication once. Terminals,
  live conversations and tunnels keep their own connections.
  `JARA_SSH_MULTIPLEX=0` turns sharing off. Claiming a session also returns
  the host checkout's branch, commit and file list, so a start without changes
  needs one round trip before the agent runs (hosts before this send none, and
  the device asks for each). Checkpoints send
  only the bytes appended to each agent session file since the host's copy,
  zstd-compressed, like every other Jara request and answer (file sync
  included) between Jara versions that support it. The remote host checks that each delta continues its copy, and at the
  end of every turn a SHA-256 of each changed file. A copy that does not match
  is repaired: after a dropped connection, from where the copy ends; only a
  copy that really differs gets the whole file.
- Taking a conversation back works the same way in reverse: the device tells
  the remote host how much of each session file it has (compared with the
  checkout path converted), receives only the lines added since, checks them
  against the host's SHA-256 and appends them. Its next checkpoint is a delta
  again; the host converts its copy to the device's checkout path first.
- The device uses its own `PATH`, toolchains, agent configuration and
  credentials, without the container's isolation: the agent's own sandbox
  settings apply.
- The remote host's `sshd_config` sets `StreamLocalBindUnlink yes` and
  `ClientAliveInterval 30`, so dropped device tunnels are noticed and replaced.

## Background daemon

`jara daemon install` sets up a per-user service (a systemd user unit, or a
launchd agent on macOS, which is unvalidated) running `jara daemon run`.
Sessions you open then start as its children instead of in your terminal:

- They outlive the terminal and logging out, and the service starts at boot
  without a login. On Linux that needs lingering: a user service runs inside
  your systemd user manager, which logind otherwise stops after your last
  logout. Install turns it on (`loginctl enable-linger`); `--no-linger` ties
  the daemon to your logins instead. A macOS launch agent runs while you are
  logged in.
- Each session still runs your `jara` binary with the environment of the
  `jara` command that started it, so `PATH`, credentials and `JARA_*`
  settings are the same as without the daemon. Without a running daemon,
  sessions start directly, as before.
- At shutdown the service stops the daemon first (`KillMode=mixed`); it asks
  each session to stop and waits up to 25 seconds. Attached folders hand
  running work to their remote host as described above.
- Local-only folders have nowhere to continue, so a session stopped while
  working records itself under `~/.local/share/jara/daemon/resume/`. The
  daemon continues it when it next starts (at boot), with a note telling the agent it was interrupted; or the next
  `jara` in that folder does. Entries older than a week are dropped.
- Suspend needs nothing special for local-only folders: the session resumes
  with the machine.

`jara daemon status` shows whether it runs, its sessions, lingering, and
interrupted work waiting to continue; `jara daemon uninstall` removes it.

## Attaching in place

`jara ps` lists every running Jara-interface session: this device's, each
remote host's own, and those other devices run in workspaces of a remote host
(reached through their runner tunnel). `jara attach` opens a window on one of
them by number, workspace or session ID, name or folder; without an argument
it picks this folder's session, the only one running, or asks.

Attaching never moves anything: it does not claim the workspace, does not
ask a busy device to hand over, does not replace a device that went offline
(that needs an explicit `jara`), and does not start a session that is not
running. Closing the window leaves the session where it runs. If the session
moves while you watch, the window follows it.

Sessions of devices that allow calls (`jara device allow`), including
local-only folders, are listed and attached through the hosts they link to;
see [network of devices](network.md). Listing a host requires the same Jara
version there (`jara deployment upgrade --remote NAME`).
