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

In a conversation, `/mcp` shows the shared list and which servers the agent
has loaded, with their state and tool count (Claude Code reports its servers
once the first turn starts it). `/mcp add`, `where`, `remove`, `import` and
`sync` take the same arguments as `jara config mcp`.

## Signing in from a conversation

`/login` shows whether Codex and Claude Code are signed in where the
conversation's agent runs (this device or its host), and how: an account, an
API key, a token Jara stored or one from the environment. Choose an agent,
then a method:

| Command | How it signs in |
| --- | --- |
| `/login codex device` | a code to enter at auth.openai.com, from any device (the default) |
| `/login codex browser` | a browser on the machine the agent runs on |
| `/login codex key` | an OpenAI API key |
| `/login codex add everywhere` | one more ChatGPT account on each host, for the account pool |
| `/login codex device everywhere` | your ChatGPT account on this device and each remote, one device code each |
| `/login codex key everywhere` | one OpenAI API key saved on this device and every remote |
| `/login claude browser` | a Claude account: open the URL, paste back the code it shows (the default) |
| `/login claude console` | the same for an Anthropic Console account, billed per use |
| `/login claude token` | a token from `claude setup-token` or an `sk-ant-api` key |
| `/login claude token everywhere` | the same, saved on this device and every remote at once |

Keys and tokens are typed hidden, stay out of the input history and are saved
on the agent's machine, as `jara auth claude` saves them. `/login cancel` or
Esc stops. A Claude sign-in replaces a token Jara stored earlier, and running
Claude Code conversations switch to new credentials on their next turn.
`CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` set where Jara runs still
take precedence; `/login` names them when they are used.

Sign-ins belong to one machine: one made on this device does not reach a
remote. A token from `claude setup-token` is made to be shared, so
`/login claude token everywhere`, or `jara auth claude --all` from a shell,
saves it here and sends it to every remote over SSH standard input; hosts
that could not be reached are listed, and running it again catches them up.
A browser sign-in is not copied, as Claude Code renews it on its own.

For Codex, `/login codex device everywhere` (or `jara auth codex --all`)
goes through the hosts in turn and shows a device code for each, so every host
keeps its own login; hosts already signed in to an account are skipped.
`/login codex key everywhere` (or `jara auth codex --all --api-key`) saves
one OpenAI API key on all of them at once. Both work through each host's
Codex directly, and Codex sessions already running there pick the sign-in up.

Signed out, the conversation still opens and Jara's own commands work; a turn
that fails because the agent is signed out says how to sign in.

## Several accounts as one

Every account signed in on a machine stays in its account pool, for Codex
and Claude Code alike: a new sign-in adds one rather than dropping the one
before. When the account in use runs out of usage, Jara puts the next one with
usage left in its place and the turn goes on, also with no window attached
(`/crunch`, scheduled prompts); the conversation says which account took over.
Conversations live on the machine, not with an account, so any account
continues any of them. An account that ran out is skipped until its usage
returns, and when every account is out the turn fails as before, so `/crunch`
waits for the earliest reset.

`/accounts` lists the pool where the conversation runs, numbered, with the one
in use and those out of usage; `/accounts use N` switches by hand and
`/accounts remove N` drops one. `/usage` and `jara usage` list each host's pool
under its limits. Codex accounts are kept as copies of `~/.codex/auth.json`,
refreshed from the live file before each switch so renewed logins are kept;
Claude Code accounts are the tokens Jara stores plus Claude Code's own login.
To add a Codex account on every host, use `/login codex add everywhere` or
`jara auth codex --all --add`; for Claude Code, `/login claude token
everywhere` with each account's token. The pool is
`~/.local/share/jara/accounts.json`, readable only by you.

Using several accounts to get past usage limits may be against the
providers' terms; that is your call.

## Usage limits

`jara usage` (or `/usage` in a conversation) lists the usage limits of the
Codex and Claude Code accounts on this device and on every remote, one host
after another; `--remote NAME` (or `/usage NAME`) shows one host. Codex
answers live. Claude Code only reports usage while it runs a turn, so each
host shows its newest report with its age.

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

Jara exports the stored value as `CLAUDE_CODE_OAUTH_TOKEN` when it starts
Claude Code, on any host or device (the Docker image's `claude` launcher does
too).
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
