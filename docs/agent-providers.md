# Agent runtimes

Jara launches one of two agent CLIs inside its managed tmux sessions:

| Runtime | Select with | Terminal UI | `--interface jara` | Authentication |
| --- | --- | --- | --- | --- |
| Codex (default) | `--agent codex` | Codex TUI | yes | `jara auth codex` (device login) |
| Claude Code | `--agent claude` | Claude Code TUI | yes | `jara auth claude` (token) |

`--agent` works with `open`, `new`, `resume`, `run`, `handoff`, and the
server-side `jara connect`. Without it, Jara uses the first default it finds:

1. the current folder's: `jara config set agent claude --workspace`;
2. the remote's: `jara config set agent claude --remote work`;
3. the global one: `jara config set agent claude`;
4. Codex.

`default` in place of an agent name removes a folder or remote override. On
plain SSH, `JARA_AGENT=claude jara connect` sets the default for that shell.
The client menu and the project browser offer *New Codex conversation* and
*New Claude Code conversation*; the browser lists only agents installed on
the host. Session lists show each session's agent.

Each agent has its own default arguments: `JARA_CODEX_ARGS` /
`jara config codex-args` for Codex and `JARA_CLAUDE_ARGS` /
`jara config claude-args` for Claude Code. Arguments after `--` apply to
whichever agent starts.

Switch agents on a workspace with `jara handoff`, and let agents call each
other through [agent delegation](agent-delegation.md).

## Shared MCP servers

Jara keeps one list of MCP servers, shared by this device and every remote,
and gives it to whichever agent it starts. You configure each server once
instead of in both Codex and Claude Code. Claude Code receives a generated
`--mcp-config` file and Codex receives `-c mcp_servers.NAME…` overrides; the
agents' own configuration files are not changed, and servers configured
there keep working alongside.

A server runs everywhere unless you limit it to some places: `local` for
this device, or remote names. After every change each remote receives its
share; a remote that is offline gets it before its next session opens there.

```sh
jara config mcp import                    # copy this machine's Claude Code and Codex servers
jara config mcp add docs --url https://docs.example/mcp --header x-token=TOKEN
jara config mcp add files --on local -- npx -y @modelcontextprotocol/server-filesystem /work
jara config mcp where docs work local     # docs only on the remote work and this device
jara config mcp where docs everywhere
jara config mcp list                      # where each runs; header and env values stay hidden
jara config mcp remove files
jara config mcp sync                      # send every remote its share now
```

The list, tokens included, is stored in this device's Jara configuration.
Servers run a command (stdio) or are reached over streamable HTTP; SSE
servers are not supported. Agents started afterwards use the new list. Codex
overrides sit on its command line, so header and env values there are visible
to other processes on the same host.

```sh
jara --agent claude                       # open or create a Claude Code session
jara new --agent claude -- --model opus   # pass arguments after -- to Claude Code
jara resume --agent claude                # claude --resume (picker)
jara resume --agent claude --latest       # claude --continue
jara run --agent claude 'fix the tests'   # claude --print in a background session
```

Both runtimes share workspaces, worktrees, session limits, idle expiration,
terminal colors, and `jara sessions`. Session labels name Claude Code sessions
so they are distinguishable in menus. Like native Codex terminals, Claude Code
terminals have no reliable busy signal, so idle expiration treats them the same.

## Claude Code authentication

Claude Code needs credentials in the remote environment. The recommended path
is a long-lived OAuth token, which works on headless hosts and uses your
Claude subscription:

1. On any machine with a browser, run `claude setup-token` and copy the
   `sk-ant-oat01-…` token it prints.
2. Store it on the Jara host:

   ```sh
   jara auth claude --remote work            # prompts with hidden input
   printf '%s' "$TOKEN" | jara auth claude --remote work --token-stdin
   ```

Jara sends the token over SSH standard input, never as a command-line
argument. It is written to `~/.local/share/jara/auth/claude-token` in the
agent's persistent home with mode `0600`. Rerun the command to replace it.
Jara also sets `hasCompletedOnboarding` in `~/.claude.json`, keeping existing
settings, so Claude Code does not start its browser login wizard.

The `claude` launcher exports the stored value as `CLAUDE_CODE_OAUTH_TOKEN`.
A value starting with `sk-ant-api` is exported as `ANTHROPIC_API_KEY` instead,
so an Anthropic API key also works. Credentials already present in the
environment take precedence. You can also SSH in and log in interactively with
`claude`; `jara doctor` accepts any of these credential sources. `jara setup`
offers the token step when the host has Claude Code installed, and the menu
lists it under *Sign-in and Git access*.

## Installation and versions

The image installs `@anthropic-ai/claude-code` from npm when the
`CLAUDE_CODE_VERSION` build setting is set. `jara environment` and guided
setup resolve and pin the latest stable release, as they do for Codex. Pass
`--claude-version VERSION` to choose a release or `--claude-version none` for
a Codex-only image. `jara deployment upgrade` adds the latest pin to guided
deployments created before Claude Code support and leaves an existing pin,
including an explicit empty one, unchanged. Compose deployments install
Claude Code only after you add `CLAUDE_CODE_VERSION=x.y.z` to their env file.

The launcher runs the installed CLI directly, independent of project
toolchains on `PATH`, and sets `DISABLE_AUTOUPDATER=1` because versions are
pinned by the deployment. To change the version, update the pin and rebuild.

## Limits

In the Jara conversation UI, both agents share one window and `/model` (F2)
lists Codex and Claude models together; see
[switching agents in the conversation UI](conversation-interface.md#codex-and-claude-in-one-conversation).
Each agent receives only its own default arguments; with `--interface jara`
arguments after `--` go to Claude Code when it is the selected agent. The
project browser and `/resume` list Codex, Claude Code, and switched
conversations. Clients pick Claude Code through `--agent`, so update hosts
running older Jara versions, which ignore the field and start Codex.

Claude Code runs as the unprivileged `agent` user inside the container, which
is the isolation boundary. It does not use Codex's sandbox. Choose its
permission mode with Claude Code arguments, for example
`-- --permission-mode acceptEdits`.
