# Jara conversation controls

These are the controls of the [conversation interface](conversation-interface.md).

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
- `@` lists the workspace's files; `@folder/` lists that folder (dotfiles after `.`), other text adds fuzzy
  search. Enter or Tab inserts `@path`; a folder inserts `@folder/` and lists it.
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
