#!/bin/sh
# Launch Claude Code with the image's Node runtime, independent of project
# toolchains that may replace node on PATH. A token saved by `jara auth claude`
# (from `claude setup-token`, or an Anthropic API key) is exported only when the
# caller has not already supplied credentials through the environment.
token_file="${JARA_DATA:-$HOME/.local/share/jara}/auth/claude-token"
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -f "$token_file" ] && [ -r "$token_file" ]; then
  IFS= read -r jara_claude_token < "$token_file" || true
  case "$jara_claude_token" in
    sk-ant-api*) ANTHROPIC_API_KEY=$jara_claude_token; export ANTHROPIC_API_KEY ;;
    sk-ant-*) CLAUDE_CODE_OAUTH_TOKEN=$jara_claude_token; export CLAUDE_CODE_OAUTH_TOKEN ;;
  esac
  unset jara_claude_token
fi
# Versions are pinned by the deployment; the image's npm prefix is read-only.
DISABLE_AUTOUPDATER=1
export DISABLE_AUTOUPDATER
target=$(readlink -f /opt/jara/claude-cli)
case "$target" in
  *.js|*.mjs|*.cjs) exec /usr/local/bin/node "$target" "$@" ;;
  *) exec "$target" "$@" ;;
esac
