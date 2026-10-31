# Automatic Codex updates

Automatic updates are **opt-in and managed on the Linux Docker host**. The agent
container never receives the host Docker socket. The updater supports guided
native Docker deployments and existing Compose deployments. A native deployment
is detected from its manifest, so it does not need Compose. Optional DinD
deployments use a separate nested daemon and require the Compose overlay flag.
The local laptop client does not administer the host or install updates.

## Behavior

`jara host update` checks the npm `latest` release of `@openai/codex`, requires an
exact stable `x.y.z` version, and only upgrades to a newer version. It does not
select prereleases or downgrade an environment.

Before changing the running environment it:

1. Checks for tmux sessions, open SSH connections and workspace operations.
2. Builds a separate candidate image from the host's deployment source and tool selections.
3. Tests the exact Codex version and real sandbox execution in an isolated
   container with no network, credentials or live data mounts. The sandbox must
   permit workspace writes and refuse writes outside its workspace.
4. Rechecks for active use after the build and takes a maintenance lease. New
   Jara workspace operations are then blocked while the container is replaced.
5. Recreates the agent, waits for Docker health and runs Jara diagnostics.
6. Records the exact successfully installed version in the deployment env file.

An open Codex terminal counts as active even when detached or waiting for input.
Updates therefore wait until users exit their terminals, rather than interrupt
them. Raw SSH/editor connections also cause deferral when detected. This is not
a policy that tries to infer whether a model is currently thinking.

If deployment or diagnostics fail, the updater attempts to restore the previous
image and leaves the configured pin unchanged. Image rollback does **not** undo
data-format migrations; maintain backups of persistent volumes independently.
Offline smoke tests do not guarantee compatibility with every future Codex release.

The maintenance lease expires after ten minutes if the updater crashes. If the
running version and env pin disagree, subsequent updates fail with an actionable
message instead of silently overwriting that disagreement. Logs identify the
previous image tag for manual recovery. Candidate and previous images are retained;
image retention/pruning is currently manual.

## Upgrade Jara itself

After installing a newer local Jara CLI, update a deployment created by guided
setup from the client:

```sh
jara deployment upgrade work
```

This transfers the source bundle embedded in that CLI, builds and validates a
candidate image with native Docker on the host, defers while the environment is
in use, and replaces the server container with rollback on failure. The host
needs no Git, Compose, or Jara installation. Workspace volumes and the
configured Codex version remain in place. After success, Jara asks whether to
remove the previous image tag used for rollback. Manual Compose deployments must
be upgraded from their host checkout.

## First install

The deployed Jara binary must include the maintenance gate. If upgrading from an
earlier Jara build, first deploy the current source when sessions have been
closed. The updater refuses to replace a container that does not support its gate.

For a guided native deployment, enter its bundled source directory, copy the
updater binary from the running container, and run the check. The native manifest
selects the Docker-only updater automatically:

```sh
cd ~/jara-deploy-work
docker cp jara-agent-work:/opt/jara/bin/jara /tmp/jara-host
sudo install -m 755 /tmp/jara-host /usr/local/bin/jara
jara host update --project jara-work --check
```

For a Compose deployment, install the Linux Jara binary on the Docker host. Either
build it with Go, or copy the compiled binary from the running container (no host
Go installation needed):

```sh
cd /path/to/jara
mkdir -p bin
JARA_AGENT_ID=$(docker compose -p jara-personal --env-file deploy/.env \
  -f deploy/compose.yaml ps -q agent)
docker cp "$JARA_AGENT_ID:/opt/jara/bin/jara" bin/jara-host
sudo install -m 755 bin/jara-host /usr/local/bin/jara
```

Use the **same project name** as the existing deployment. Run a read-only check:

```sh
jara host update --project jara-personal \
  --compose deploy/compose.yaml --env-file deploy/.env --check
```

To perform an update once, omit `--check`. For a deliberately chosen stable
release, add `--version 0.166.0`. The updater still refuses downgrades.

## Enable an hourly systemd timer

For a Compose deployment, from the checkout on the Docker host:

```sh
sudo install -m 644 deploy/systemd/jara-codex-update@.service /etc/systemd/system/
sudo install -m 644 deploy/systemd/jara-codex-update@.timer /etc/systemd/system/
sudo install -d -m 755 /etc/jara

sudo tee /etc/jara/jara-personal.env >/dev/null <<EOF
JARA_COMPOSE_FILE="$PWD/deploy/compose.yaml"
JARA_ENV_FILE="$PWD/deploy/.env"
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now jara-codex-update@jara-personal.timer
```

For a guided native deployment, use absolute paths to its bundled settings in
`/etc/jara/PROJECT.env` (replace `host-user` with the account used during setup):

```dotenv
JARA_ENV_FILE=/home/host-user/jara-deploy-work/deploy/.env
JARA_NATIVE_MANIFEST=/home/host-user/jara-deploy-work/deploy/.jara-native.json
```

Then enable the timer with the same project name, for example
`jara-codex-update@jara-work.timer`. The native manifest selects Docker commands;
no Compose plugin is needed.

Checks run hourly with up to five minutes of randomized delay. If the environment
is in use, the job logs a deferral and checks again on the next timer run. No
scheduler is enabled merely by building or starting the agent container.

The supplied service runs as root to access Docker. For a deployment owned by a
different Docker-authorized user, use a systemd drop-in to set `User=` so updated
env files retain that user's ownership. The service uses the saved host checkout;
it does not pull Jara source or change other tool version selections. Local source
changes present in that checkout are included in candidate builds.

For another Compose project, install a corresponding `/etc/jara/PROJECT.env` and
enable `jara-codex-update@PROJECT.timer`. Different projects should have separate
env files, Compose project names and published ports.

## Inspect or disable

```sh
systemctl list-timers 'jara-codex-update@*'
journalctl -u jara-codex-update@jara-personal.service
sudo systemctl start jara-codex-update@jara-personal.service
sudo systemctl disable --now jara-codex-update@jara-personal.timer
```

The native updater needs Docker Engine and network access for the stable release
lookup and image build. Compose deployments also need the Compose plugin. An
update briefly recreates the container after it is unused; persistent volumes
and host identity remain in place. Do not delete its volumes when updating.
