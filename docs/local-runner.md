# Agents on your device (experimental)

In the Jara conversation interface, `jara` runs the agent on the machine you
are sitting at: against your local checkout, with your local Codex and Claude
Code CLIs and sign-in. The remote host keeps the shared copy. After every turn,
and every 20 seconds during long turns, it receives the checkout's commits and
edits, every conversation of the workspace, and pending scheduled prompts.

Any device can watch a running conversation live or continue it. When your
laptop goes to sleep in the middle of a turn, the remote host continues the
turn by itself. When the laptop wakes, its window follows the conversation and
takes it back as soon as the remote host is idle.

This is an experimental branch feature. It needs a remote host running the same
Jara version (`jara deployment upgrade --remote NAME`), and a Unix client
(Linux or macOS) with `codex` on `PATH`. `claude` is used when installed. Only
the Jara interface takes part; native Codex and Claude Code terminals
(`--interface codex`) keep running on the remote host.

## Commands

```sh
jara                        # run here when Codex is installed; otherwise on the host
jara --run-remote           # run this session on the remote host instead
JARA_RUN_REMOTE=1 jara      # the same, for every session in this shell
jara new                    # a new conversation, running here
jara runner status          # where the agent runs, online or not, last checkpoint
jara --take-over            # take over although the other device still looks online
```

`JARA_DEVICE` overrides the device name used in leases (the host name by
default). Several projects, or several Git worktrees of one project, are
independent workspaces: each runs wherever it was last opened.

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
  saw them, along with pending scheduled prompts.
- **Run.** The session runs as a background `jara _runner-daemon`. `/detach`
  leaves it running; `/quit` while idle ends it and hands the workspace over.
- **Link.** One SSH connection per device carries a reverse Unix-socket forward
  to the live session and `jara _runner-link`, which renews the lease with a
  heartbeat every few seconds. Through it, `jara connect` on the remote host
  (a phone in Termius, for example) and other devices attach to the live
  session.

## Sleep, network loss and coming back

- **Going to sleep with Linux and systemd.** Jara holds a logind delay lock.
  Just before suspend, if a turn is running or scheduled work is waiting, it
  saves a checkpoint (within about three seconds) and tells the remote host,
  which continues at once.
- **Disappearing without warning** (macOS sleep, network loss, a crash). When
  the heartbeats stop for 30 seconds, a watcher on the remote host takes over
  if a turn was running or work was scheduled. The interrupted turn continues
  with a note that the device went offline, from the last checkpoint: at most
  about 20 seconds of work is redone.
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

Work a device did after its last checkpoint, before another machine replaced
it, is set aside before catching up:

- Uncommitted work goes into a Git stash named `jara: local work set aside …`.
- Commits the remote copy lacks stay on a `jara/set-aside-<time>` branch, and
  the checkout is reset to the last commit it synchronized.
- In copied (non-Git) folders, changed files are copied to
  `~/.local/share/jara/set-aside/<id>/<time>/`.

Edits you made yourself on a device that was not running the agent merge like
`jara sync pull`, which refuses conflicting changes.

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

## Limits of this experiment

- While a device holds the workspace, the remote host's checkout is a mirror.
  Edits made there directly, for example over plain SSH, are overwritten by the
  next checkpoint.
- A window queue (prompts waiting in the composer) and `/crunch` tasks stay
  with the session that held them; scheduled prompts move.
- Handing back to a waking device ends other windows attached to the remote
  host's session, such as a phone; reconnect them to follow the conversation.
- Each checkpoint opens several SSH connections, so connection multiplexing
  (`ControlMaster`) in your SSH config makes them faster.
- The device uses its own `PATH`, toolchains, agent configuration and
  credentials, without the container's isolation: the agent's own sandbox
  settings apply.
- The remote host's `sshd_config` sets `StreamLocalBindUnlink yes` and
  `ClientAliveInterval 30`, so dropped device tunnels are noticed and replaced.
