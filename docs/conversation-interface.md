# Jara conversation interface preview

`jara`, `jara new`, `jara resume`, `jara sessions`, and `jara connect` open
conversations in Jara's text interface by default:

```sh
jara new
jara resume
jara resume --latest
```

For plain SSH, use `jara connect --new WORKSPACE_ID`.
The client and remote host must both include this feature. Upgrade the host
with `jara deployment upgrade --remote NAME` after installing a newer client.

Jara runs Codex app-server over stdio inside its existing managed tmux session.
Disconnecting SSH leaves that process running. Reattach through `jara` or the
project browser. An existing terminal retains the interface it started with;
use `jara new` to open a Jara conversation alongside a Codex terminal.
`--interface codex` selects the native Codex terminal interface.
To switch an idle persistent Jara conversation to Codex, connect with
`jara connect --interface codex WORKSPACE_ID`; Jara hands off the saved thread
to a Codex terminal. A running turn, another attached client, or scheduled work
keeps the persistent session in place so its work can finish safely.
Jara starts Codex terminals with `--no-daemon` when Codex supports it. Without
it, Codex 0.167 and later keep a terminal's conversation in a shared background
server after the terminal exits, so Jara cannot resume it. If Jara reports that
a conversation "already has an active writer", close its Codex terminal, or run
`codex app-server daemon stop` on the host when no terminal is open.

When the local CLI opens an associated workspace in the Jara interface, the
Jara conversation UI runs on the client and connects over SSH to Codex
app-server in the remote workspace. Its session persists if the local CLI
disconnects, so an active turn keeps running in the background and can be
resumed by opening the workspace again. Closing the UI with `/quit` during an
active turn or `/detach` leaves the remote agent running. An SSH disconnect
does the same if a turn is active. When a disconnected turn finishes, Jara
stops the app-server and releases the workspace lock; the saved conversation is
resumed when you open the workspace again. An idle session stops on `/quit`
or idle Ctrl-C. An SSH disconnect or `/detach` keeps an idle session so a
client, such as a phone, can reconnect. `/stop` always ends it.

A detached, idle session (no running turn, attached client, or pending
scheduled prompt or crunch task) does not block other work on the workspace.
Automatic updates, `jara sync push` (including the push offered when you open the
workspace), and workspace deletion stop it first, and `jara sync pull` proceeds
alongside it. The next connection restarts the session on its saved
conversation. A busy session still blocks these operations.

The local CLI reconnects by itself when its connection to the session drops,
for example after a network change or when an automatic update stops an idle
session. It shows that it is reconnecting, queues prompts typed meanwhile and
resumes the conversation. For this reason, automatic updates are not deferred by
idle sessions whose attached clients are all local CLIs. If the session was
stopped on purpose, such as with `/stop` from another client, the CLI exits
instead. Clients in direct SSH sessions and worktree sessions do not reconnect. Direct SSH connections use the remote `jara connect`
CLI and keep new workspace sessions in the same persistent service. Existing
managed tmux sessions keep their current attach behavior.

Sign in using `jara auth codex`. Codex continues to own the agent runtime,
credentials, sandbox, tools, configuration, and saved conversations. This preview
uses the app-server shipped with Codex 0.166.0. It adds no inference proxy or
new authentication flow.

## Interactive terminal

Interactive terminals use a full-screen conversation view: the transcript fills
the screen from the top, with styled user/agent/tool messages, an activity row,
and a bordered composer above the model and reasoning controls. The workspace
path and current Git branch appear above the composer. The transcript scrolls
independently of your draft and reflows when the terminal is resized. Codex's
concise reasoning summaries update the activity row.
Subagent activity appears inline with its delegation prompt, lifecycle updates,
and any status or result text the agent reports in the parent thread. While a
conversation has running subagents, a strip above the input box lists the main agent and each
subagent with its status (the latest four); a finished one leaves it after 30 seconds, and `/agents` lists all.
Ctrl+↓ and Ctrl+↑ switch the transcript to the next or previous agent there, tapping
a row shows that agent, and Esc returns to the main agent. A subagent's view
shows its own transcript (prompts, replies, commands with output, and file changes); statuses are rechecked
until each finishes. A subagent finishing while the main agent is idle wakes it (unless the agent continues
itself). Raw reasoning text is not shown. Plain output remains available for pipes and `TERM=dumb` terminals.

While a subagent is shown, what you type is sent to it, and slash commands still
apply to the conversation (start a message with `//` to send a literal slash).
A Codex subagent receives the message in its own thread, even while the main
agent is working. Claude Code cannot deliver a message straight to a subagent,
so Jara asks the main agent to forward it with its `SendMessage` tool. That
request appears in the main conversation and waits in the queue while the main
agent is busy.

Type `/` in the composer to open command suggestions. Filter by typing, navigate
with the arrow keys, and accept a suggestion with Tab or Enter. Acceptance fills
the composer; it does not execute the command. Escape dismisses the dropdown.
`/model ` and `/resume ` offer contextual choices from the most recently loaded
`/models` and `/sessions` results. Enter submits completed input; Shift+Enter,
Alt+Enter, or Ctrl+J inserts a newline. PageUp and PageDown scroll the
transcript, as does the mouse wheel, while the composer keeps its draft.
Because Jara captures the mouse wheel, hold Shift while dragging over history
to use the terminal's native text selection, then copy with its usual shortcut.

Submitting bare `/resume` opens a recent-session picker ordered newest first.
Use the arrow keys to choose a conversation, Enter to open it, or Escape to
return to the chat. `/resume ID` still opens a specific conversation directly.

`/models` and bare `/model` open an interactive model picker. Use F2 from any
prompt to open it directly. `/reasoning` and F3 open the selected model's
supported reasoning choices. Use the arrow keys and Enter to choose, or Escape
to close. Shift+Up raises reasoning effort and Shift+Down lowers it.
Model and reasoning changes made during a turn apply to the next prompt Jara
starts, including queued prompts. The controls show the selection below the
composer. For Claude Code's default entry, the model control shows the concrete
model Claude Code reports once it starts.

`/default` opens the same picker to set the default model for new conversations
on this host; `/default ID` or `/default N` sets it directly. The default model
also picks the provider: with a Claude default, new conversations start on
Claude Code even when the configured agent is Codex. Only an explicit choice
(`--agent`, or "New … conversation" in the project browser) overrides it, and
only for the conversation the session opens with; `/new` afterwards follows the
default model. Jara
also saves the model in the agent's own configuration (`model` in Codex's
`config.toml`, or in Claude Code's `settings.json`), so that agent's terminals
on the host use it too.

`/fallback` picks a model, typically from the other provider, that takes over
when the current one reaches its usage limit. `/crunch` and `/goalcrunch`
switch to it once instead of waiting for the reset, and wait only if the
fallback reaches its limit too. An ordinary turn that hits the limit is retried
once on the fallback. `/fallback off` turns it off. Both choices are stored on
the host in `~/.local/share/jara/models.json`.
When Codex is working, an animated activity row shows the latest concise
progress summary when available; otherwise it reports whether Codex is thinking,
running a command, applying changes, or waiting for input. The row reads
`✿ Pondering… (12s · ctrl+c to interrupt)`: an animation picked per turn (an
opening flower, a rising sun or an explosion; `JARA_SPINNER=flower|sun|explosion`
keeps one), the label under a sweeping shimmer with a per-turn verb, and muted
details for elapsed time, token progress, and how to interrupt.
`JARA_REDUCED_MOTION=1` keeps the row but stops the shimmer. Message headers show their timestamps when Codex
provides them.

Bracketed paste inserts multiline text without submitting it. The composer has
input history and [Vim editing](tui.md#vim-editing-in-conversations). Text typed while an agent
is streaming remains a draft if submission is unavailable; control commands such
as `/cancel`, `/answer`, and `/approve` remain usable during a turn.

The theme follows the terminal background; `JARA_THEME=dark` or
`JARA_THEME=light` selects an explicit palette in the Jara environment.
`NO_COLOR` retains the layout with monochrome styling. Set
`JARA_CHAT_PLAIN=1` to use the line-oriented interface inside a terminal.

## Conversation controls

Messages, shell commands, the prompt queue, slash commands, approvals, and
cancellation are described in [conversation controls](conversation-controls.md).

## Agent activity and detached sessions

Jara's conversation interface publishes `starting`, `running`, `waiting` (for
approval or an answer), and `idle` activity to its managed terminal. Terminal listings show
this separately from the number of connected viewers. Native Codex terminals
without this integration retain an unknown agent state.

Detached-session expiration preserves a known active Jara agent, including one
waiting for approval. Activity metadata is trusted only while the recorded
Jara conversation process is alive beneath a pane in the same tmux session.
Stale metadata cannot keep a dead agent alive indefinitely. Idle conversations
remain subject to the configured expiration policy; use
`JARA_SESSION_IDLE_TIMEOUT=0` to disable expiration entirely.

Approval requests show the request details and available file-change data.
In the full-screen view, Enter or `y` in an empty input box allows the oldest
approval and `n` denies it; in plain line mode, answer `y` or `n`.
In auto mode the agent's own reviewer decides approval requests first, and one
that reaches you is approved automatically after 3 minutes without an answer.
With auto mode off, only an explicit `/approve` grants approval. Structured non-secret questions
show numbered options and queue additional question sets. `/cancel` rejects
pending questions and interrupts the turn. Secret questions and unsupported
interactions such as MCP elicitation receive a protocol error instead of being
silently accepted. Turn failures are displayed with their
reported status. Unanswered protocol requests time out after 30 seconds;
streaming inference turns have no fixed timeout.

Explicit server rejections of a new turn, new conversation, or resume request
are displayed without closing the interface. A failed switch keeps the previous
loaded conversation selected. If no conversation has opened yet, use `/new` or
`/resume ID` before sending a prompt. Runtime disconnections and uncertain turn
submission timeouts still stop the interface; do not assume a timeout proves
that no work started.

`--model`, `--sandbox`, `--ask-for-approval`, `--search`, `-c`/`--config`,
`--enable`, and `--disable` arguments are translated or forwarded to app-server.
`--dangerously-bypass-approvals-and-sandbox` (alias `--yolo`) selects
`sandbox_mode="danger-full-access"` and `approval_policy="never"`, preserving
the flag's meaning for configured host defaults and invocation arguments.
Other Codex arguments produce an error before a managed terminal is created.
This also applies to host defaults configured through `jara config codex-args`.

## Preview limitations

The plain fallback uses a line-oriented composer. The interactive view styles
common Markdown headings, lists, and fenced code blocks; it does not implement
HTML, image attachments, or secret question entry. Use the Codex interface
for those workflows. Native Codex history remains the fallback from the project
browser's **All Codex conversations** action. Conversations outside managed
workspaces run directly and do not survive an SSH disconnect.

The app-server protocol is experimental. Keep Codex pinned when relying on this
preview. Offline subprocess tests cover handshake, streamed replies, approval
rejection, interruption, resumption, and shutdown without using account tokens
or making model calls.

Additional offline checks cover multiline submission, draft limits and discard,
status/help during active turns, approval scope visibility, and early input
during initialization. Question tests cover explicit multi-question replies,
queued request IDs, cancellation, secret rejection, and Unicode wrapping at
40 columns. History tests cover folder filtering, bounded search, incomplete
results, and numbered choices. A subprocess regression verifies continued use
after a rejected resume. An isolated real tmux server verifies live-agent
retention, stale metadata expiration, legacy session expiration, and cleanup
refusing to proceed when process inspection fails.

A 40×20 PTY subprocess test exercises canonical terminal input, a burst-pasted
multiline draft, streamed replies, numbered questions, explicit approvals,
Ctrl-C interruption, and exit. The installed Codex 0.166.0 metadata smoke test
passes initialization, exact-folder history filtering, and model metadata:

```sh
JARA_REAL_CODEX_TEST=1 go test ./internal/jara \
  -run '^TestInstalledCodexAppServerMetadataSmoke$' -count=1 -timeout=40s
```

It starts no inference turns and prints no account or conversation data. A
second, opt-in test drives the installed, logged-in Claude Code through the
agent router: a reply, model and context reporting, an image, `/btw`,
`/compact`, and `/rewind`. It sends a few short prompts to Claude Haiku:

```sh
JARA_REAL_CLAUDE_TEST=1 go test ./internal/jara \
  -run '^TestInstalledClaudeCodeThroughTheRouter$' -count=1 -timeout=10m
```
 These
checks cover terminal behavior and protocol metadata; physical phone apps and
live inference remain outside their verified scope.

Reference: [Codex app-server](https://learn.chatgpt.com/docs/app-server).
Runtime ownership and future adapter requirements are described in
[agent providers](agent-providers.md).

## Changes pane and motion

When the terminal is at least 120 columns wide and 18 rows tall, file changes
appear in a pane to the right of the conversation, newest edit first. On
smaller terminals, such as phones, the pane never opens by itself, because
it would squeeze the conversation. **F4** or `/diff` shows it over the whole
screen instead: PgUp/PgDn and the mouse wheel scroll it, and **✕**, Esc,
**F4** or `/diff` closes it. Typing closes it and goes to the input box.

- In a Git workspace the pane shows the **uncommitted** changes: tracked files
  against `HEAD`, staged or not, plus untracked files, most recently modified
  first. It refreshes when a conversation opens, after each turn and file edit,
  after `!` commands, on `/diff`, and every 10 seconds while it is on screen,
  so a commit empties it and edits made outside the agent appear too. Very
  large change sets show the 100 newest files. Outside Git, the pane lists
  the edits the agent made in this conversation, as reported by the agent.
- Each file shows its path and `+added −removed` counts, then its hunks with
  tinted additions and deletions. Code is syntax-highlighted by the file's
  language, using GitHub's light or dark colors to match the terminal.
- On wide terminals the pane opens automatically with the agent's first
  edit. Its **✕** button, **F4**, or `/diff` hides it; it stays hidden until
  F4 or `/diff` shows it again. **Alt+PgUp/PgDn** (or **F5/F6**) scrolls it.
- **[ ask ]** starts a question about the changes: it fills the input box with
  "About your changes to …:" naming the changed files, so you type the
  question and press Enter. Text already in the input box is kept.
- History loaded with `/resume` fills the agent's edits without animation.

Code blocks in the transcript are highlighted the same way: by the fence
language (```` ```go ````) or, for file-change diffs, by the changed file.
Highlighting is per line, so a string or comment spanning lines may be
colored as code after its first line.

While a turn runs, a highlight sweeps across the activity label. New changes wipe into the pane from the top, and the newest file
briefly glows. The welcome mark twinkles. Only changed rows are redrawn, and
idle sessions without visible animation do not redraw. Set
`JARA_REDUCED_MOTION=1` to turn all animation off; `NO_COLOR` also disables
it.

## Codex and Claude in one conversation

The conversation UI runs on Jara's agent router (`jara _agent-server`). It
speaks the app-server protocol to the UI:

- Codex conversations are proxied to `codex app-server`.
- Claude Code conversations are served by driving `claude` in stream-json
  mode.

Start a conversation on Claude Code with `jara new --interface jara --agent
claude`, or switch at any time:

- `/model` (F2) lists Codex models followed by Claude Code entries
  (`claude/default`, `claude/opus`, `claude/sonnet`, `claude/haiku`,
  `claude/fable`). F3 offers Claude's effort levels, `low` to `max`.
- Picking another model from the same agent changes the model for the next
  turn. A Claude model change uses Claude Code's runtime model switch. An
  effort change restarts Claude Code on the same session.
- Picking the other agent's model moves the conversation to that agent. The UI
  keeps the same conversation ID. The receiving agent gets a briefing with the
  earlier exchange (messages and edited files); the working tree already holds
  every change. Switching back to an agent that worked on the conversation
  before resumes its own session, with its full earlier context, and briefs it
  only on the turns it missed.
- A reasoning effort the new model lacks becomes its closest level (Codex's
  `minimal` becomes Claude's `low`; Claude's `max` becomes Codex's `xhigh`).

The briefing replays each earlier turn: messages, commands run with their
exit status and last output line, plans, edited files, and the active goal.
Hidden context (internal reasoning, tool call details) does not carry over,
because the agents' histories are not interchangeable. An active goal moves
with the conversation: Codex pursues it natively, and the router pursues it
for Claude Code.

For Claude Code turns:

- Replies stream as usual. Shell commands, file edits, and other tools appear
  as tool items. Edits show real diffs from Claude's structured patches,
  including in the changes pane.
- Claude Code's multiple-choice questions, including multi-select ones,
  arrive as ordinary questions, and your own text is always accepted.
- Claude Code runs in its `auto` permission mode unless `/auto off` is set;
  any remaining tool permissions arrive as ordinary approvals: `/approve` or `/deny`.
- Ctrl+C interrupts the turn, and `/plan` switches Claude Code to plan mode.
- Task/Agent subagents are viewable like Codex ones (whole replies, Claude Code 2.1.275+; kept while the
  session runs). When a background subagent finishes after the main agent's turn ended, the main agent
  continues with its result: Claude Code's own follow-up is shown as a turn, and if none starts within a
  few seconds Jara tells the main agent itself. Subagents not on screen are rechecked while they run.
- Claude Code does not expose thinking text in stream-json mode, even with
  `showThinkingSummaries`. The activity line instead shows live progress such
  as *Thinking · ~330 tokens*.
- `/goal` works as with Codex. Each turn carries the objective, and after each
  turn the router starts another until one of these happens:
  - Claude ends a reply with `GOAL COMPLETE: …` or `GOAL BLOCKED: …`;
  - the token budget is reached;
  - you interrupt (the goal is paused);
  - 50 automatic turns have run.

  Time and tokens are tracked as for Codex goals. A message you send while an
  automatic turn runs is queued and goes before the next continuation.
- Arguments after `--` with `--interface jara --agent claude` are passed to
  every Claude Code process the conversation starts, after the
  `jara config claude-args` defaults.

Conversations that involved Claude Code are kept under
`~/.local/share/jara/agent-threads/`. They appear in `/resume` and in the
project browser, labeled with their current agent (*(switched)* marks mixed
ones), and each conversation is listed once. A resumed conversation continues
on the agent that ran its last segment, and its history labels every reply
with the agent that wrote it. From the browser, the native interface reopens
the latest segment with `claude --resume` or `codex resume` in the
conversation's directory.
