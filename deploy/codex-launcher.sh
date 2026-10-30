#!/bin/sh
# Project toolchains may replace PATH, but never Codex's own Node runtime.
# Docker's Unix socket requires networking in Codex's Linux sandbox. This
# applies only to opt-in DinD environments; explicit user -c flags come last.
if [ "${JAG_DOCKER:-0}" = 1 ]; then
  set -- -c sandbox_workspace_write.network_access=true "$@"
fi
exec /usr/local/bin/node /usr/local/lib/node_modules/@openai/codex/bin/codex.js "$@"
