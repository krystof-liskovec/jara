# Native Codex conversation migration

Investigated 2026-10-30 against installed **codex-cli 0.166.0** and its
version-pinned official protocol source.

## Finding

No verified, supported public CLI interface imports an existing local Codex
conversation into a different remote environment as a native resumable thread.
This required feature is blocked by the verified upstream interface. Jag's
`migrate` command fails explicitly, without moving or editing session files.

Checked `codex --help`, `codex resume --help`, `codex app-server --help`, and
`codex migrate-rollouts --help`.

- `resume --remote` connects to a server and resumes a conversation already
  available there. It does not upload local history.
- `migrate-rollouts` upgrades local history storage format, not host location.
- Markdown export is not a documented native restore format.
- `thread/resume.history` is explicitly marked unstable, for Codex Cloud, and
  “DO NOT USE.” Its server-local rollout `path` alternative is also unstable.

Sources: [version-pinned protocol](https://github.com/openai/codex/blob/rust-v0.166.0/codex-rs/app-server-protocol/src/protocol/v2/thread.rs#L340),
[official commands](https://learn.chatgpt.com/docs/developer-commands),
[app-server](https://learn.chatgpt.com/docs/app-server).

## Related supported workflow

The desktop application documents host handoff with conversation and Git-state
transfer between matching projects. That does not establish a supported CLI API
for Jag. [Remote handoff documentation](https://learn.chatgpt.com/docs/remote-connections).

## Acceptance criteria for a future implementation

1. Use a supported interface and record compatible Codex versions.
2. Require an idle source and retain the original conversation.
3. Validate destination workspace, execution directory, tools and integrations.
4. Produce a native conversation selectable by remote `codex resume`.
5. Report path/capability differences and fail clearly if fidelity is unavailable.
6. Only offer summary-based continuation as an explicitly selected separate mode.
