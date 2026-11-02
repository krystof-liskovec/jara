# Jara terminal interface

Jara reads the terminal's background color once using the OSC 11 query, then
builds visible card and selection surfaces from that color. Text and accent
colors adapt for dark, light and middle gray backgrounds. The query happens
before entering the menu so the response does not affect key navigation.

If the terminal does not answer the query, Jara uses `JARA_TMUX_BACKGROUND`,
then the host's configured background, then its built-in fallback. Set
`JARA_TMUX_BACKGROUND=#RRGGBB` on the host when a phone terminal does not
support the query and its theme differs from the configured background.
`JARA_THEME=light` and `JARA_THEME=dark` explicitly select fixed palettes;
`NO_COLOR` disables styling.

The interface keeps a violet breadcrumb, an inset search field, and a bottom
shortcut bar. Desktop lists are centered at a readable width; phones use the
available width, with compact spacing on short screens. Selection never
changes row content. Search includes metadata and titles; long lists paginate.

## Projects and conversations

The SSH entry point and `jara sessions` open Projects. Jara combines registered
workspaces with saved Codex conversation directories. Conversations inside a
registered workspace's subdirectories belong to that project. Project names use
the repository name; owner and host remain visible in the secondary line to
distinguish repositories with the same name. Projects sort by the most recently
updated conversation. Empty registered projects remain available for new chats.

Projects reloads history and terminal counts when you return to the list. Use
**Refresh projects** to fetch chats created elsewhere while the menu was open.
History failures display **History unavailable**, rather than a misleading zero
count; partial results show a lower-bound count and **history incomplete**.
The Codex history fallback remains available, with the loading error visible.

Separate checkouts of the same repository have distinct labels. New workspaces
record the originating device and local path: duplicates appear as `jara · laptop`
and `jara · desktop`. Multiple checkouts on one device include enough of their
source paths to distinguish them. Existing workspaces without device metadata
use their creation time, with a short workspace reference when times coincide.
Labels stay with their checkout as the activity order changes, including inside
the project. The secondary line shows the current Git branch and, when recorded,
the source path alongside conversation and terminal counts.

Opening a Git folder on a new device checks the host for ready checkouts of the
same repository. Choose an existing checkout to link this folder to it, or clone
a separate checkout. The chooser uses the same device and path labels as Projects.
The link is remembered for this local folder; future `jara` invocations reopen it.
Setup's `open` choice uses this flow too. Explicit `--clone` skips discovery.
For a folder already linked to a checkout, use `jara --choose-workspace` or
**Choose checkout for this folder** in `jara menu` to select another one.

Linking preserves the remote branch and files. Local working edits can be synced
after confirmation, or later with `jara sync push`; remote edits are preserved
and conflicting changes stop the transfer. Initial sync uses the local committed
tree as its baseline, so joining does not replace a remote checkout with local
commits. Later file changes and commits are tracked by the usual sync baseline.

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
