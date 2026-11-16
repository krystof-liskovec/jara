#!/bin/sh
set -eu
jara_ssh_user=${JARA_SSH_USER:-agent}
case "$jara_ssh_user" in
  [a-z_]*) ;;
  *) echo 'JARA_SSH_USER must start with a lowercase letter or underscore.' >&2; exit 1 ;;
esac
case "$jara_ssh_user" in
  *[!a-z0-9_-]*) echo 'JARA_SSH_USER contains unsupported characters.' >&2; exit 1 ;;
esac
if [ "${#jara_ssh_user}" -gt 32 ] || [ "$jara_ssh_user" = root ]; then
  echo 'JARA_SSH_USER must be at most 32 characters and cannot be root.' >&2
  exit 1
fi
if getent passwd "$jara_ssh_user" >/dev/null 2>&1; then
  if [ "$(id -u "$jara_ssh_user")" != 1001 ]; then
    echo 'JARA_SSH_USER conflicts with an existing container account.' >&2
    exit 1
  fi
elif getent passwd agent >/dev/null 2>&1; then
  usermod --login "$jara_ssh_user" agent
else
  echo 'The Jara service account is missing.' >&2
  exit 1
fi
gateway_ports=${JARA_GATEWAY_PORTS:-no}
session_idle_timeout=${JARA_SESSION_IDLE_TIMEOUT:-24h}
case "$gateway_ports" in
  no|clientspecified) ;;
  *) echo 'JARA_GATEWAY_PORTS must be no or clientspecified.' >&2; exit 1 ;;
esac
tmux_background=${JARA_TMUX_BACKGROUND:-}
if [ -n "$tmux_background" ]; then
  case "$tmux_background" in
    \#[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]) ;;
    *) echo 'JARA_TMUX_BACKGROUND must be a six-digit RGB color such as #262626.' >&2; exit 1 ;;
  esac
fi
if ! jara _validate-session-settings; then
  echo 'Jara session settings are invalid.' >&2
  exit 1
fi
if [ ! -s /run/jara/authorized_keys ] || [ ! -f /run/jara/authorized_keys ]; then
  echo 'Supply a nonempty deploy/authorized_keys file containing your public SSH key(s).' >&2
  exit 1
fi
mkdir -p /run/sshd /var/lib/jara/ssh
# Never copy/chown through paths in the agent's writable home. Keep SSH keys
# in an image/container-owned directory and replace the file atomically.
mkdir -p /etc/ssh/authorized_keys
chmod 755 /etc/ssh/authorized_keys
chown root:root /etc/ssh/authorized_keys
authorized_keys_tmp=$(mktemp /etc/ssh/authorized_keys/.agent-keys.XXXXXXXX)
cp /run/jara/authorized_keys "$authorized_keys_tmp"
chmod 644 "$authorized_keys_tmp"
chown root:root "$authorized_keys_tmp"
mv -f "$authorized_keys_tmp" "/etc/ssh/authorized_keys/$jara_ssh_user"
# /tmp persists across docker restart. Reject symlinks and unexpected owners;
# never repair this path with privileged chmod/chown that could follow a link.
if [ -L /tmp/jara-cache ]; then
  echo 'Unsafe /tmp/jara-cache symlink; remove it from the trusted Docker host before restarting.' >&2
  exit 1
elif [ -e /tmp/jara-cache ]; then
  if [ ! -d /tmp/jara-cache ] || [ "$(stat -c %u /tmp/jara-cache)" != "$(id -u "$jara_ssh_user")" ]; then
    echo 'Unsafe /tmp/jara-cache path or owner; remove it from the trusted Docker host before restarting.' >&2
    exit 1
  fi
else
  cache_tmp=$(mktemp -d /tmp/.jara-cache.XXXXXXXX)
  chmod 700 "$cache_tmp"
  mv -T "$cache_tmp" /tmp/jara-cache
  chown "$jara_ssh_user":agent /tmp/jara-cache
fi
if [ ! -f /var/lib/jara/ssh/ssh_host_ed25519_key ]; then
  ssh-keygen -q -t ed25519 -N '' -f /var/lib/jara/ssh/ssh_host_ed25519_key
fi
chown "$jara_ssh_user":agent /home/agent
umask 077
if [ -s /run/secrets/gitlab_token ]; then
  runuser -u "$jara_ssh_user" -- jara _auth gitlab "${GITLAB_HOST:-gitlab.com}" < /run/secrets/gitlab_token
fi
if [ -s /run/secrets/gitea_token ]; then
  : "${GITEA_URL:?Set GITEA_URL when supplying a Gitea token}"
  runuser -u "$jara_ssh_user" -- jara _auth gitea "$GITEA_URL" < /run/secrets/gitea_token
fi
mkdir -p /run/jara
# SSH sessions do not inherit arbitrary Docker environment variables. Store the
# validated array outside the writable agent home for each Jara session launch.
printf '%s' "${JARA_CODEX_ARGS:-[]}" > /run/jara/codex-args.json
jara _validate-codex-args < /run/jara/codex-args.json
chmod 644 /run/jara/codex-args.json
printf '%s' "${JARA_CLAUDE_ARGS:-[]}" > /run/jara/claude-args.json
jara _validate-codex-args < /run/jara/claude-args.json
chmod 644 /run/jara/claude-args.json
if [ -n "$tmux_background" ]; then
  printf '%s' "$tmux_background" > /run/jara/tmux-background
  chmod 644 /run/jara/tmux-background
fi
printf 'GatewayPorts %s\nAllowUsers %s\n' "$gateway_ports" "$jara_ssh_user" > /run/jara/sshd-env.conf
# Standby hosts: keys of the hosts ranked above this one, each forced to run
# only Jara's standby operations, whatever the user's peer file says.
printf 'AuthorizedKeysCommand /opt/jara/bin/jara _peer-keys %%h\nAuthorizedKeysCommandUser %s\n' "$jara_ssh_user" >> /run/jara/sshd-env.conf
printf '%s' 'SetEnv JARA_PEER_KEYS_COMMAND=1 PATH=/home/agent/.local/share/mise/shims:/usr/local/share/mise/shims:/opt/jara/bin:/opt/jara/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin LANG=C.UTF-8 LC_CTYPE=C.UTF-8 GOTOOLCHAIN=local GOCACHE=/tmp/jara-cache/go-build GOMODCACHE=/tmp/jara-cache/go-mod NPM_CONFIG_CACHE=/tmp/jara-cache/npm' >> /run/jara/sshd-env.conf
: > /run/jara/docker.env
if [ "${JARA_DOCKER:-0}" = 1 ]; then
  if [ ! -S /run/jara-docker/docker.sock ]; then
    echo 'Docker requested but its socket is missing; include deploy/compose.docker.yaml and wait for the docker service to become healthy.' >&2
    exit 1
  fi
  printf '%s' ' JARA_DOCKER=1 DOCKER_HOST=unix:///run/jara-docker/docker.sock BUILDX_CONFIG=/tmp/jara-cache/docker-buildx' >> /run/jara/sshd-env.conf
  echo 'export JARA_DOCKER=1 DOCKER_HOST=unix:///run/jara-docker/docker.sock BUILDX_CONFIG=/tmp/jara-cache/docker-buildx' > /run/jara/docker.env
fi
printf '\n' >> /run/jara/sshd-env.conf
chmod 644 /run/jara/sshd-env.conf /run/jara/docker.env
if [ "$session_idle_timeout" != 0 ]; then
  runuser -u "$jara_ssh_user" -- env HOME=/home/agent JARA_SESSION_IDLE_TIMEOUT="$session_idle_timeout" /usr/local/bin/jara _session-janitor &
fi
/usr/sbin/sshd -t
effective_sshd_config=$(/usr/sbin/sshd -T)
for required_ssh_setting in 'authenticationmethods publickey' 'passwordauthentication no'; do
  if ! printf '%s\n' "$effective_sshd_config" | grep -Fxq "$required_ssh_setting"; then
    echo "Unsafe SSH configuration: expected '$required_ssh_setting'." >&2
    exit 1
  fi
done
exec /usr/sbin/sshd -D -e
