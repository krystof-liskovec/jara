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
conversation has subagents, a strip above the input box lists the main agent
and each subagent with its status (the latest four, plus `/agents` for all).
Ctrl+↓ and Ctrl+↑ switch the transcript to the next or previous agent, tapping
a row shows that agent, and Esc returns to the main agent. A subagent's view
shows its own transcript (prompts, replies, commands with output, and file
changes) and refreshes while it runs. Raw reasoning text is not shown. Plain
output remains available for pipes and `TERM=dumb` terminals.

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

Enter one line of text to send a message. Replies and command output stream into
the terminal. `/help` lists the controls:

- Prefix a line with `!` (for example, `!git status`) to run a shell command in
  the workspace and show its output in the conversation.

While a turn is running, type a follow-up and press Tab to queue it. Jara sends
queued prompts one at a time after the current turn. Use `/queue` to inspect the
queue, which is also shown above the composer. Use `/queue edit N` to load a
queued prompt into the composer, then press Enter to save the edit or Escape to
cancel it, and `/queue clear` removes queued prompts for this conversation. In a
persistent session the session keeps the queue: queued prompts run one at a time
whenever the conversation is idle, also after the window closes, every attached
window shows them, and they move with the conversation to another machine.
Prompts typed while a window reconnects wait in that window until it is back.

- `/compose` starts a multiline message; `/send` submits it and `/discard`
  clears it. Blank lines and indentation are preserved. `/help`, `/status`,
  and `/quit`, `/detach`, `/stop` remain available while composing. Start a literal slash line
  with `//` (for example, `//path` sends `/path`).
- `/new` starts another saved conversation in the same terminal.
- `/sessions [query]` lists recent saved conversations in the current folder.
  Search matches names, previews, and IDs. Results load asynchronously, show
  at most eight choices, and report when the search is incomplete.
- `/resume` opens the recent sessions for the current working directory;
  `/resume ID` resumes a specific conversation, and `/resume N` opens a numbered
  result from the last `/sessions` list.
- `/status` shows the connection, conversation, turn state and pending approvals,
  including while a turn is running. A live terminal alone does not mean the
  agent is working.
- `/goal <objective>` sets a durable goal for this conversation. Bare `/goal`
  shows its status and usage; `/goal pause`, `/goal resume`, and `/goal clear`
  manage its lifecycle. `/goal edit <objective>` replaces the objective.
  Goals are stored per conversation and may continue automatically between
  turns. If Goals are disabled, set `goals = true` under `[features]` in Codex's
  `config.toml` and restart Codex.
- `/plan` switches this conversation to Codex Plan mode for current and future
  turns. `/plan PROMPT` enables the mode and starts planning that prompt;
  `/plan off` returns to Default mode, and `/plan status` reports the mode.
- `/auto` (on by default) lets an automatic reviewer approve or deny agent
  actions instead of asking you: Codex routes approval requests to its
  `auto_review` reviewer, and Claude Code runs in its `auto` permission mode.
  `/auto off` returns to asking you, and `/auto status` reports the mode.
  Shift+Tab (or tapping the control) toggles it too; the controls under the
  input show `⏵⏵ Auto mode on` or `◇ Auto mode off`. The choice applies from
  the next turn and resets to on when Jara reopens. In auto mode, an approval
  the reviewer still passes to you is allowed automatically after 3 minutes
  without an answer, so an unattended session does not stay stuck; the
  approval row counts down (`Auto-allow in 2:59`). Answering, or turning auto
  mode off, stops the countdown. The countdown runs only while a Jara client
  is attached, and restarts when one reattaches.
- `/agents` lists the conversation's subagents; `/agents N` shows subagent N
  and `/agents main` returns to the main agent.
- `/btw QUESTION` asks a quick side question about the conversation, even
  while a turn is running. The answer appears in a "By the way" message and is
  not added to the conversation. Codex answers in an ephemeral, read-only fork
  of the thread; Claude Code answers in a forked, unsaved session without tools.
  A completed Plan item is shown in chat.
- The controls bar shows how much of the model's context window is left once
  the agent reports usage (`◔ Context 72% left`; `/status` shows the token
  counts). `/compact` compacts the context now: Codex runs its compaction, and
  Claude Code runs its own `/compact`; progress shows in the chat and activity
  label. A conversation using half its context window is compacted after it
  idles (Codex 25m, Claude Code 55m), before the prompt cache expires, even
  while detached; `/compact idle DURATION` sets the current agent's delay and
  `/compact idle off` disables it. `/clear` starts a new conversation.
- `/rewind` opens a list of this conversation's prompts (`/rewind N` picks one
  directly). The agent forgets the chosen prompt and everything after it, the
  transcript reloads, and the prompt returns to the input box to edit and send.
  A conversation that only ran on Codex is reverted natively with Codex's
  `thread/revert`; any other conversation continues in a fresh session briefed
  on the remaining turns. Files in the working tree are not rolled back.
- Typing `@` and part of a path lists matching workspace files, found by the
  app-server's fuzzy file search; Enter or Tab inserts `@path`.
- Images join a prompt as `[Image #N]` placeholders: paste or drop an image
  file's path, press Ctrl+V with an image on the clipboard (`wl-paste`,
  `xclip`, or `pngpaste` on the machine running the interface), or use
  `/image PATH`. PNG, JPEG, GIF, and WebP images up to 5 MB are read where the
  interface runs and sent with the prompt to either provider.
- Prompt files in `~/.codex/prompts`, `~/.claude/commands`, and the project's
  `.claude/commands` work as custom slash commands with either provider
  (subfolders become `/folder:name`). Jara fills in `$ARGUMENTS` and `$1`…`$9`
  and appends arguments a prompt does not reference. Built-in commands win
  over custom ones with the same name.
- When a turn that ran for 15 seconds or more ends, or an approval or question
  waits for you, Jara rings the terminal bell and sends a desktop notification
  (OSC 9, passed through tmux when its `allow-passthrough` is on). The bell
  also flags the tmux window. `/alerts off` silences this for the session.
- On phones the layout shrinks its chrome, controls become tap targets, `y`/`n`
  answer approvals, `\` before Enter adds a newline, and `/notify URL` sends
  push notifications; see [phone access](plain-ssh.md#working-from-a-phone).
- `/diff` shows or hides the changes pane. In plain output, which has no
  pane, it lists the uncommitted changes (or, outside Git, the conversation's
  file changes, oldest first).
  `/export [PATH]` saves the conversation as
  Markdown, by default as `jara-conversation-DATE-TIME.md` in the workspace.
- `/crunch PROMPT` works on the task until it is complete, then stops. If the
  agent stops because usage is exhausted, Jara waits for the reported
  rate-limit reset and continues from the saved conversation. `/goalcrunch
  OBJECTIVE` sets a goal and continues it until the goal is marked complete,
  including across usage-limit resets. Both commands stop when their task is
  complete. Use `/crunch status` or `/crunch stop` to inspect or stop the active
  task. Crunch tasks require a persistent Jara session and usage-window data.
  Both commands work with both agents:
  - Codex reports its ChatGPT windows.
  - For Claude Code, the agent router reports the five-hour and weekly windows
    from Claude's rate-limit events, so crunch waits for the right reset.
  - `/goalcrunch` drives a Claude goal itself; the router's automatic goal
    continuation stands down while it does.
- `/schedule in DURATION PROMPT` or `/schedule at RFC3339 PROMPT` schedules a
  one-time prompt for this conversation. Use `/schedule list` to review jobs and
  `/schedule cancel ID` to cancel a pending one. Scheduled prompts run when the
  conversation is idle, including after you disconnect. Scheduling requires a
  persistent Jara session.
- `/models` loads the runtime's model catalog without interrupting a turn.
  `/model N` selects a listed model, and `/model ID` selects an explicit model.
  `/reasoning` lists effort values supported by the selected model, while
  `/reasoning EFFORT` selects one directly. F2 and F3 open the interactive
  model and reasoning pickers. These selections apply to this and subsequent
  turns in this conversation; they do not change account-wide defaults.
- `/cancel` or Ctrl-C interrupts a running turn. Ctrl-C while idle stops the
  session. `/quit` stops an idle session but leaves an active agent running;
  `/detach` leaves it running whether it is active or idle. `/stop` always ends it.
- `/approve` approves the oldest pending command or file approval once.
- `/deny` rejects that approval; the agent can continue.
- `/answer Q VALUE` answers question `Q` of the current set with an option
  number, comma-separated numbers for a multi-select question (`/answer 1 1,3`)
  or, when offered, free text. Nothing is sent until every question in the
  request has an explicit answer. In the full-screen view the menu above the
  input box answers the current question: ↑↓ choose, Enter selects, and in a
  multi-select question Enter or Space checks and Submit sends. Text typed and
  sent with Enter is your own answer, added to any checked options. `n` in an
  empty input box, once you chose an option with the keys, starts a note sent
  with that option instead (Claude Code gets it as the note). Escape stops it.
- `/quit` stops an idle persistent session. During an active turn it closes the
  UI and leaves the agent running until that turn finishes. Use `/stop` to stop
  immediately and release its workspace lock.
- `/detach` closes the UI and leaves the remote session running. Reopen the
  workspace or use `jara connect --interface jara WORKSPACE_ID` to attach again.

Use `/help` to discover commands without leaving the conversation. Commands are
written on separate lines so they remain readable in narrow SSH terminals.
For a phone SSH client, paste or type directly into the composer, then press
Enter to send. Outside compose mode, unknown slash commands display help
guidance instead of sending an accidental prompt.

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
- Subagents started with the Task or Agent tool can be viewed like Codex
  subagents. Their replies arrive as whole messages rather than streamed text,
  and need Claude Code 2.1.275 or later; older versions show only their tool
  calls. Their transcripts are kept only while the session runs.
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
