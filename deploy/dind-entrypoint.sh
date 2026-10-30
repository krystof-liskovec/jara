#!/bin/sh
set -eu
# The upstream image declares its data directory as VOLUME. Initialize ownership
# here because legacy Docker builders discard chown changes below a VOLUME.
chown 1001:1001 /home/rootless/.local/share/docker /home/rootless/docker-run
chmod 700 /home/rootless/docker-run
# Explicit dockerd argument bypasses the upstream TCP listener defaults.
# Keep the socket outside /run, which RootlessKit copies into its namespace.
exec su-exec rootless /usr/local/bin/dockerd-entrypoint.sh dockerd --host=unix:///home/rootless/docker-run/docker.sock "$@"
