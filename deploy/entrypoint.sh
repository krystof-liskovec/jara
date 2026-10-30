#!/bin/sh
set -eu
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
mv -f "$authorized_keys_tmp" /etc/ssh/authorized_keys/agent
# /tmp persists across docker restart. Reject symlinks and unexpected owners;
# never repair this path with privileged chmod/chown that could follow a link.
if [ -L /tmp/jara-cache ]; then
  echo 'Unsafe /tmp/jara-cache symlink; remove it from the trusted Docker host before restarting.' >&2
  exit 1
elif [ -e /tmp/jara-cache ]; then
  if [ ! -d /tmp/jara-cache ] || [ "$(stat -c %u /tmp/jara-cache)" != "$(id -u agent)" ]; then
    echo 'Unsafe /tmp/jara-cache path or owner; remove it from the trusted Docker host before restarting.' >&2
    exit 1
  fi
else
  cache_tmp=$(mktemp -d /tmp/.jara-cache.XXXXXXXX)
  chmod 700 "$cache_tmp"
  mv -T "$cache_tmp" /tmp/jara-cache
  chown agent:agent /tmp/jara-cache
fi
if [ ! -f /var/lib/jara/ssh/ssh_host_ed25519_key ]; then
  ssh-keygen -q -t ed25519 -N '' -f /var/lib/jara/ssh/ssh_host_ed25519_key
fi
chown agent:agent /home/agent
umask 077
if [ -s /run/secrets/gitlab_token ]; then
  runuser -u agent -- jara _auth gitlab "${GITLAB_HOST:-gitlab.com}" < /run/secrets/gitlab_token
fi
if [ -s /run/secrets/gitea_token ]; then
  : "${GITEA_URL:?Set GITEA_URL when supplying a Gitea token}"
  runuser -u agent -- jara _auth gitea "$GITEA_URL" < /run/secrets/gitea_token
fi
mkdir -p /run/jara
printf '%s' 'SetEnv PATH=/home/agent/.local/share/mise/shims:/usr/local/share/mise/shims:/opt/jara/bin:/opt/jara/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin GOTOOLCHAIN=local GOCACHE=/tmp/jara-cache/go-build GOMODCACHE=/tmp/jara-cache/go-mod NPM_CONFIG_CACHE=/tmp/jara-cache/npm' > /run/jara/sshd-env.conf
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
/usr/sbin/sshd -t
exec /usr/sbin/sshd -D -e
