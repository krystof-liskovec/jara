# Jara conversation interface preview

Start a new conversation with Jara's text interface:

```sh
jara new --interface jara
jara resume --interface jara
jara resume --latest --interface jara
```

For plain SSH, use `jara connect --new --interface jara WORKSPACE_ID`.
The client and remote host must both include this feature. Upgrade the host
with `jara deployment upgrade --remote NAME` after installing a newer client.

Jara runs Codex app-server over stdio inside its existing managed tmux session.
Disconnecting SSH leaves that process running. Reattach through `jara` or the
project browser. An existing terminal retains the interface it started with;
use `jara new --interface jara` to try this alongside a Codex terminal.
`--interface codex` selects the existing Codex terminal interface.

When the local CLI opens an associated workspace with `--interface jara`, the
Jara conversation UI runs on the client and connects over SSH to Codex
app-server in the remote workspace. Its session persists if the local CLI
disconnects, so an active turn keeps running in the background and can be
resumed by opening the workspace again. Closing the UI with `/quit` during an
active turn or `/detach` leaves the remote agent running. An SSH disconnect
does the same if a turn is active. When a disconnected turn finishes, Jara
stops the app-server and releases the workspace lock; the saved conversation is
resumed when you open the workspace again. An idle session stops on `/quit`,
idle Ctrl-C, or an SSH disconnect. `/detach` keeps an idle session explicitly,
and `/stop` always ends it. Direct SSH connections use the remote `jara connect`
CLI and keep new workspace sessions in the same persistent service. Existing
managed tmux sessions keep their current attach behavior.

Sign in using `jara auth codex`. Codex continues to own the agent runtime,
credentials, sandbox, tools, configuration, and saved conversations. This preview
uses the app-server shipped with Codex 0.166.0. It adds no inference proxy or
new authentication flow.

## Interactive terminal

Interactive terminals use a full-screen conversation view with a persistent
workspace/model/activity header, styled user/agent/tool messages, and a bordered
composer. The transcript scrolls independently of your draft and reflows when
the terminal is resized. Narrow terminals use the same controls with a compact
layout. Message groups have clearer spacing, and Codex's concise reasoning
summaries appear inline as progress updates, such as command or exploration
activity. The interface does not display raw reasoning text. Plain output remains
available for pipes and `TERM=dumb` terminals.

Type `/` in the composer to open command suggestions. Filter by typing, navigate
with the arrow keys, and accept a suggestion with Tab or Enter. Acceptance fills
the composer; it does not execute the command. Escape dismisses the dropdown.
`/model ` and `/resume ` offer contextual choices from the most recently loaded
`/models` and `/sessions` results. Enter submits completed input; Alt+Enter or
Ctrl+J inserts a newline. PageUp and PageDown scroll the transcript while the
composer keeps its draft.

Submitting bare `/resume` opens a recent-session picker ordered newest first.
Use the arrow keys to choose a conversation, Enter to open it, or Escape to
return to the chat. `/resume ID` still opens a specific conversation directly.

`/models` and bare `/model` open an interactive model picker. Use F2 from any
prompt to open it directly. `/reasoning` and F3 open the selected model's
supported reasoning choices. Use the arrow keys and Enter to choose, or Escape
to return to the chat. The model and reasoning controls remain visible below
the composer, and the choices apply to this and later turns in the conversation.
When Codex is working, an animated activity row shows whether it is thinking,
running a command, applying changes, or waiting for input. Message headers show
their timestamps when Codex provides them.

Bracketed paste inserts text, including multiple lines, without submitting it.
Input history and cursor editing work in the composer. Text typed while an agent
is streaming remains a draft if submission is unavailable; control commands such
as `/cancel`, `/answer`, and `/approve` remain usable during a turn.

The theme follows the terminal background; `JARA_THEME=dark` or
`JARA_THEME=light` selects an explicit palette in the Jara environment.
`NO_COLOR` retains the layout with monochrome styling. Set
`JARA_CHAT_PLAIN=1` to use the line-oriented interface inside a terminal.

## Conversation controls

Enter one line of text to send a message. Replies and command output stream into
the terminal. `/help` lists the controls:

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
- `/answer Q VALUE` answers question number `Q` in the current question set.
  Use an option number or, when offered, free text. For example, `/answer 1 2`
  chooses option 2 for question 1. Nothing is preselected or sent until every
  question in that request has an explicit answer.
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
Only an explicit `/approve` grants approval. Structured non-secret questions
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

It starts no inference turns and prints no account or conversation data. These
checks cover terminal behavior and protocol metadata; physical phone apps and
live inference remain outside their verified scope.

Reference: [Codex app-server](https://learn.chatgpt.com/docs/app-server).
Runtime ownership and future adapter requirements are described in
[agent providers](agent-providers.md).
