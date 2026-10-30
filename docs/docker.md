# Optional Docker inside an environment

Jara can provide Docker CLI, Buildx and Compose backed by a separate rootless
Docker daemon for each Compose project. The laptop still only needs Jara and SSH.
No host Docker socket is mounted. Docker CLI/plugins are bundled in the agent
image; the daemon and socket access are opt-in.

## Deploy

On the Linux Docker host (Compose 2.24 or newer):

```sh
./bin/jara environment --codex-version 0.166.0 --docker --out deploy/.env
# Add your PUBLIC key(s) to deploy/authorized_keys.
docker compose -p jara-personal --env-file deploy/.env \
  -f deploy/compose.yaml -f deploy/compose.docker.yaml up -d --build --wait
```

For an existing deployment, close sessions first, add `JARA_DOCKER=1` and
`DOCKER_VERSION=29.9.2` to its env file, then run the command with its existing
project name. Keep including **both Compose files** in all deployment commands.
As with ordinary Jara deployments, SSH binds to host loopback by default; adjust
`JARA_SSH_BIND` or use your existing SSH jump host. Use a different project name
and SSH port for each independent environment.

`jara doctor` checks the enabled daemon. In an SSH shell, editor terminal or Codex:

```sh
docker info
docker build -t my-app .
docker compose up -d
docker run --rm -v "$PWD:/work" -w /work alpine ls
```

Docker-enabled environments launch Codex with
`-c sandbox_workspace_write.network_access=true`, the supported
[workspace sandbox networking setting](https://learn.chatgpt.com/docs/config-file/config-reference).
Codex 0.166.0 blocks the Docker Unix socket when sandbox networking is disabled.
This enables ordinary outbound network access as well as Docker; filesystem
sandboxing stays enabled. An explicit user `-c` override takes precedence. Ordinary
environments retain Codex's default networking policy.

The daemon sees `/home/agent` at the same path as the agent, so workspace bind
mounts work, including private files. Paths elsewhere in the agent container
(such as `/tmp`) are not shared with the daemon. Nested root maps to the agent's
UID; other nested users may require the usual bind-mount ownership adjustments.
Use named volumes for databases. Images, containers and named volumes live in a
separate persistent daemon volume; workspace files remain in `agent-home`.

## Ports and lifecycle

The agent shares the daemon sidecar's network namespace. A nested service published
with `127.0.0.1:8080:8080` is reachable at `localhost:8080` in the agent. Forward it
from your laptop using the existing Jara command:

```sh
jara forward 8080
```

Nested published ports are not automatically exposed on the Docker host. Use SSH
forwarding for laptop access. Privileged ports below 1024 can require additional
rootless configuration; prefer ports such as 8080.

Recreating only the agent preserves the daemon and its running nested containers:

```sh
docker compose -p jara-personal --env-file deploy/.env \
  -f deploy/compose.yaml -f deploy/compose.docker.yaml \
  up -d --no-deps --no-build --force-recreate --wait agent
```

If the daemon is recreated, also recreate the agent so it joins the new network
namespace; nested containers may need their own restart policies or `compose up`.
A full `down` stops both services; `down -v` deletes their persistent data.

Automatic Codex updates must retain the overlay:

```sh
jara host update --project jara-personal --compose deploy/compose.yaml \
  --compose-extra deploy/compose.docker.yaml --env-file deploy/.env
```

For the systemd timer, add
`JARA_COMPOSE_EXTRA=/absolute/path/to/jara/deploy/compose.docker.yaml` to
`/etc/jara/jara-personal.env`. Codex updates recreate only the agent. Docker updates
are manual: change the exact `DOCKER_VERSION` pin, rebuild both services in a
maintenance window, and recreate the agent alongside the daemon.

## Isolation and host requirements

The daemon runs as UID 1001 in a rootless user namespace. Its outer container
requires `privileged: true`, as specified by [Docker's rootless DinD instructions](https://docs.docker.com/engine/security/rootless/tips/#rootless-docker-in-docker).
A short root bootstrap fixes volume ownership before dropping privileges. The
agent remains unprivileged and connects through a private Unix socket; there is
no TCP Docker API listener, host socket, host PID namespace or host network mode.

This is a trusted developer environment, **not a hostile multi-tenant boundary**.
Docker API access grants control over the nested daemon and its shared agent
home, including credentials. Commands sent to Docker can modify shared files
outside the current Codex workspace; Codex's filesystem sandbox cannot constrain
those daemon-side effects. Enable this only for environment owners you trust.
For hostile workloads, use a separate VM per owner.

The host must allow unprivileged user namespaces and privileged containers.
Rootless networking/storage and cgroup limits depend on the Linux host; cgroup
resource controls may be unavailable without systemd delegation. See
[Docker's rootless limitations](https://docs.docker.com/engine/security/rootless/tips/#limiting-resources).
Jara stores Buildx state in its writable temporary cache so sandboxed builds work;
registry login configuration stays in the persistent home. Run `docker login`
from your SSH shell when needed.

## Verification

`scripts/docker_e2e.py` exercises the actual Docker images, SSH, real Codex sandbox,
Buildx build, private bind mounts, nested Compose service and named volume,
localhost and SSH forwarding, and agent recreation. It makes no model calls and
imports no credentials. Build images with the Compose command above under the
project name `jara-dind-validation`, or supply `JARA_TEST_IMAGE` and
`JARA_DIND_TEST_IMAGE`. The test uses disposable projects and removes their volumes.
