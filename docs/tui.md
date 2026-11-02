# Jara terminal interface

Every Jara menu uses a midnight canvas, slate cards and an indigo selection that
covers both title and metadata. A violet breadcrumb header, inset search field
and bottom shortcut bar frame the list. Green accents identify live and working
status. Desktop lists are centered at a readable width; phones use the available
width, with compact spacing on short screens. Selection never changes row
content. Search includes metadata and titles; long lists paginate to fit.

Set `JARA_THEME=light` for the light palette. The default is the dark palette.
Both themes paint complete backgrounds for consistent contrast, and `NO_COLOR`
disables styling. These settings apply where Jara runs: set them on the remote
host for the SSH interface. The colored interface uses RGB terminal colors,
which modern terminals including Termius support.

## Projects and conversations

The SSH entry point and `jara sessions` open Projects. Jara combines registered
workspaces with saved Codex conversation directories. Conversations inside a
registered workspace's subdirectories belong to that project. Project names use
the repository name; owner and host remain visible in the secondary line to
distinguish repositories with the same name. Projects sort by the most recently
updated conversation. Empty registered projects remain available for new chats.

Opening a project shows conversation names, falling back to the prompt preview.
The secondary line contains the agent, age and any matched live terminal count.
A reported active Codex status appears as `working`. Selecting a conversation
with one live terminal reattaches immediately; multiple live terminals open a
chooser. Otherwise Jara resumes that specific saved conversation.

Registered workspaces resume in managed tmux terminals. Conversations outside
registered workspaces resume directly in the SSH terminal using their saved
working directory. These direct processes do not have tmux's disconnect survival.

`New conversation` starts Codex in a registered workspace. `Project terminals`
shows its terminal processes, while the root `Terminals` action includes all
terminals on Jara's socket. Terminal records are not conversation history.
`All Codex conversations` provides Codex's native picker as a fallback.

## History and terminal identity

Jara reads conversation summaries through Codex's documented app-server
`initialize`, `initialized` and paginated `thread/list` protocol. It does not parse
rollout JSONL or depend on SQLite table names. Loading has a deadline, and the
native picker remains available if history loading fails.

Conversation providers own listing and resume commands. Summaries carry an agent
label independently of tmux metadata, allowing future CLIs to share project lists.
Codex is the only provider currently implemented.

Conversations resumed by this browser record their ID in the managed terminal.
Older terminals can be linked through explicit resume command arguments or a
child process's `CODEX_THREAD_ID`. Ambiguous IDs and shared working directories
are never treated as proof of identity. Unmatched terminals remain accessible
through terminal management. A live count describes matched terminal processes;
it does not imply the model is currently generating a response. The `working`
label is displayed only when Codex reports an active thread.

Sources: [Codex app-server](https://learn.chatgpt.com/docs/app-server),
[Codex commands](https://learn.chatgpt.com/docs/developer-commands).

## Keyboard navigation

Up/Down move the selection; Right and Enter open the selected item. Left returns
to the parent menu. Backspace also returns, except while editing search text;
after the search is empty, another Backspace returns. These keys work with both
standard and application-mode terminal arrow sequences.

Ctrl-C closes the entire Jara interface, including through nested menus and an
SSH client menu. It never means Back. Jara restores terminal state and exits with
status 130. Inside an attached agent terminal, that agent handles its own keys.
The `q` and Escape shortcuts retain their existing Back/search-clear behavior.
