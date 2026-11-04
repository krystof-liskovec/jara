# Agent runtime providers

Jara's built-in conversation interface currently launches Codex only. The
`--interface` option selects the conversation UI (`codex` or `jara`); it does
not select an agent provider. The built-in UI speaks the Codex app-server
protocol, so Jara does not advertise its event handling as a generic provider
protocol.

The small runtime descriptor records the executable, supported configuration
options, protocol name, and authentication owner. Codex remains responsible
for its own authentication through the Codex CLI. Jara's saved Git hosting
credentials are a separate feature and do not supply model-provider
credentials. No authentication behavior changes here.

Provider lookup accepts only the implemented `codex` runtime. Names such as
`claude` are rejected as unsupported; Jara does not launch another CLI under a
Codex protocol adapter or imply that such integration exists.

A future runtime needs more than an executable and argument mapper before it
can power the built-in interface. Its adapter must define normalized request
and event semantics for messages, tool calls, approvals, cancellation, and
conversation identity. Jara can then translate those semantics to the UI and
own provider-specific configuration and authentication deliberately. Until
that boundary exists, adding another launch command alone is insufficient.
