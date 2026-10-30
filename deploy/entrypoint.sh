#!/bin/sh
set -eu
if [ ! -s /run/jag/authorized_keys ] || [ ! -f /run/jag/authorized_keys ]; then
  echo 'Supply a nonempty deploy/authorized_keys file containing your public SSH key(s).' >&2
  exit 1
fi
mkdir -p /run/sshd /var/lib/jag/ssh
# Never copy/chown through paths in the agent's writable home. Keep SSH keys
# in an image/container-owned directory and replace the file atomically.
mkdir -p /etc/ssh/authorized_keys
chmod 755 /etc/ssh/authorized_keys
chown root:root /etc/ssh/authorized_keys
authorized_keys_tmp=$(mktemp /etc/ssh/authorized_keys/.agent-keys.XXXXXXXX)
cp /run/jag/authorized_keys "$authorized_keys_tmp"
chmod 644 "$authorized_keys_tmp"
chown root:root "$authorized_keys_tmp"
mv -f "$authorized_keys_tmp" /etc/ssh/authorized_keys/agent
# /tmp persists across docker restart. Reject symlinks and unexpected owners;
# never repair this path with privileged chmod/chown that could follow a link.
if [ -L /tmp/jag-cache ]; then
  echo 'Unsafe /tmp/jag-cache symlink; remove it from the trusted Docker host before restarting.' >&2
  exit 1
elif [ -e /tmp/jag-cache ]; then
  if [ ! -d /tmp/jag-cache ] || [ "$(stat -c %u /tmp/jag-cache)" != "$(id -u agent)" ]; then
    echo 'Unsafe /tmp/jag-cache path or owner; remove it from the trusted Docker host before restarting.' >&2
    exit 1
  fi
else
  cache_tmp=$(mktemp -d /tmp/.jag-cache.XXXXXXXX)
  chmod 700 "$cache_tmp"
  mv -T "$cache_tmp" /tmp/jag-cache
  chown agent:agent /tmp/jag-cache
fi
if [ ! -f /var/lib/jag/ssh/ssh_host_ed25519_key ]; then
  ssh-keygen -q -t ed25519 -N '' -f /var/lib/jag/ssh/ssh_host_ed25519_key
fi
chown agent:agent /home/agent
umask 077
if [ -s /run/secrets/gitlab_token ]; then
  runuser -u agent -- jag _auth gitlab "${GITLAB_HOST:-gitlab.com}" < /run/secrets/gitlab_token
fi
if [ -s /run/secrets/gitea_token ]; then
  : "${GITEA_URL:?Set GITEA_URL when supplying a Gitea token}"
  runuser -u agent -- jag _auth gitea "$GITEA_URL" < /run/secrets/gitea_token
fi
mkdir -p /run/jag
printf '%s' 'SetEnv PATH=/home/agent/.local/share/mise/shims:/usr/local/share/mise/shims:/opt/jag/bin:/opt/jag/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin GOTOOLCHAIN=local GOCACHE=/tmp/jag-cache/go-build GOMODCACHE=/tmp/jag-cache/go-mod NPM_CONFIG_CACHE=/tmp/jag-cache/npm' > /run/jag/sshd-env.conf
: > /run/jag/docker.env
if [ "${JAG_DOCKER:-0}" = 1 ]; then
  if [ ! -S /run/jag-docker/docker.sock ]; then
    echo 'Docker requested but its socket is missing; include deploy/compose.docker.yaml and wait for the docker service to become healthy.' >&2
    exit 1
  fi
  printf '%s' ' JAG_DOCKER=1 DOCKER_HOST=unix:///run/jag-docker/docker.sock BUILDX_CONFIG=/tmp/jag-cache/docker-buildx' >> /run/jag/sshd-env.conf
  echo 'export JAG_DOCKER=1 DOCKER_HOST=unix:///run/jag-docker/docker.sock BUILDX_CONFIG=/tmp/jag-cache/docker-buildx' > /run/jag/docker.env
fi
printf '\n' >> /run/jag/sshd-env.conf
chmod 644 /run/jag/sshd-env.conf /run/jag/docker.env
/usr/sbin/sshd -t
exec /usr/sbin/sshd -D -e
