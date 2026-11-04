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

Sign in using `jara auth codex`. Codex continues to own the agent runtime,
credentials, sandbox, tools, configuration, and saved conversations. This preview
uses the app-server shipped with Codex 0.166.0. It adds no inference proxy or
new authentication flow.

## Conversation controls

Enter one line of text to send a message. Replies and command output stream into
the terminal. `/help` lists the controls:

- `/new` starts another saved conversation in the same terminal.
- `/resume ID` resumes a saved conversation in the current working directory.
- `/cancel` or Ctrl-C interrupts a running turn. Ctrl-C while idle exits.
- `/approve` approves the oldest pending command or file approval once.
- `/deny` rejects that approval; the agent can continue.
- `/quit` exits and stops this app-server process.

Approval requests show the request details and available file-change data.
Only an explicit `/approve` grants approval. Unsupported interactive requests,
including structured user questions and MCP elicitation, receive a protocol
error instead of being silently approved. Turn failures are displayed with their
reported status. Unanswered protocol requests time out after 30 seconds;
streaming inference turns have no fixed timeout.

`--model`, `--sandbox`, `--ask-for-approval`, `--search`, `-c`/`--config`,
`--enable`, and `--disable` arguments are translated or forwarded to app-server.
`--dangerously-bypass-approvals-and-sandbox` (alias `--yolo`) selects
`sandbox_mode="danger-full-access"` and `approval_policy="never"`, preserving
the flag's meaning for configured host defaults and invocation arguments.
Other Codex arguments produce an error before a managed terminal is created.
This also applies to host defaults configured through `jara config codex-args`.

## Preview limitations

This first interface uses a line-oriented composer and plain text output.
It does not provide Codex's full-screen editing, rich Markdown, slash-command
catalog, image attachments, or structured question forms. Use the Codex interface
for those workflows. Native Codex history remains the fallback from the project
browser's **All Codex conversations** action. Conversations outside managed
workspaces run directly and do not survive an SSH disconnect.

The app-server protocol is experimental. Keep Codex pinned when relying on this
preview. Offline subprocess tests cover handshake, streamed replies, approval
rejection, interruption, resumption, and shutdown without using account tokens
or making model calls.

Reference: [Codex app-server](https://learn.chatgpt.com/docs/app-server).
