# Agent delegation

Codex and Claude Code sessions on a Jara host can hand work to each other, or
to another instance of themselves. Jara provides this through its own MCP
server, `jara mcp`, which both agents load. Delegation works the same in both
directions and needs no extra setup.

## How it is enabled

When Jara starts any agent session, it registers the server in the agent's
user configuration if it is missing:

- Codex: a `[mcp_servers.jara]` table in `~/.codex/config.toml` with
  `tool_timeout_sec = 3600`.
- Claude Code: an `mcpServers.jara` entry in `~/.claude.json`.

Existing entries named `jara` are never modified. On the host,
`jara delegation status` shows the state and recent tasks, and
`jara delegation disable` removes Jara's entries and keeps them from being
re-added. `jara delegation enable` restores them. Restart running agent
sessions after either change. Check the registration with `codex mcp list` or
`claude mcp list`.

## Tools

| Tool | Purpose |
| --- | --- |
| `delegate_task` | Start `claude` or `codex` with a self-contained prompt. Returns the result, or a task ID if the task is still running. |
| `task_status` | Wait for a task and report its status, worktree changes, and result. |
| `continue_task` | Send a follow-up prompt to a finished task's conversation (`claude --resume`, `codex exec resume`). |
| `list_tasks` | Recent tasks on the host. |
| `merge_task` | Commit a worktree task's changes on its branch and merge it into the caller's checkout. |
| `cancel_task` | Stop a running task. |

`delegate_task` options:

- `isolation`:
  - `worktree` (default in Git repositories): the task gets branch
    `jara/<task-id>` and its own checkout under `~/.local/share/jara/worktrees`,
    created from the caller's `HEAD`. Uncommitted caller changes are not
    visible to it. Bring results back with `merge_task`, which leaves any
    conflicts in place for the caller to resolve.
  - `shared`: the task works directly in the caller's checkout.
- `permission`: delegated runs cannot answer permission prompts, so the level
  is chosen up front.

  | Level | Claude Code | Codex |
  | --- | --- | --- |
  | `read-only` | `--permission-mode plan` | `sandbox_mode="read-only"` |
  | `edit` (default) | `acceptEdits`; prompts are refused | `workspace-write` sandbox, no approvals |
  | `full` | `bypassPermissions` | `--dangerously-bypass-approvals-and-sandbox` |

  `full` is limited only by the container.
- `model`, `cwd`, and `wait_seconds`: the default wait is 45 seconds and the
  maximum 3000. Longer work returns a task ID to poll with `task_status`.

## Visibility and limits

Each run executes in its own tmux session (`jara-task-…`), tagged with its
workspace and agent, so `jara sessions` and the project browser show it next
to ordinary sessions. Attach to watch progress; finished runs stay visible
until expiry. Task state, prompts, logs, and results are kept under
`~/.local/share/jara/tasks/<task-id>/`.

The server refuses new tasks when one of these limits is reached:

- `JARA_MAX_DELEGATED_TASKS` running tasks (default 4).
- A nesting depth above `JARA_MAX_DELEGATION_DEPTH` (default 2: an agent you
  started may delegate, and its delegate may delegate once more).

Depth is found from the `JARA_TASK_ID` of the delegating agent's process
ancestry, because Codex does not pass arbitrary environment variables to MCP
servers. Running tasks also count toward the host's session limit.

## Handoff

`jara handoff` is the user-facing counterpart: it opens the current workspace
with the other agent (or `--agent NAME`). The new agent starts with a briefing
that includes:

- the Git branch, status, and diff summary;
- recent commits;
- pending delegated tasks with unmerged branches;
- an optional `--note`.

Agent conversation formats are not interchangeable, so the repository, not the
previous transcript, carries the state. The project menu also offers
*Hand off this workspace*.
