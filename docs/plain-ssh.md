# Plain SSH and phone access

The environment runs ordinary OpenSSH. Jara is only needed on the server; a phone
or other SSH client does not need the local Jara CLI. Your phone can attach to the
same persistent Codex terminals you use from your laptop.

## Termius setup

1. Add a host with your deployment's reachable hostname, published SSH port
   (2222 by default), username `agent`, and an authorized SSH key.
2. Verify the SSH host fingerprint through trusted host access, as described in
   the [deployment guide](../README.md#deploy-on-a-linux-docker-host).
3. Connect and run `jara connect` to browse workspaces and running sessions.

For direct access to the menu whenever you connect, set the host's **Startup
Command** to `jara connect`. Termius documents this feature in its
[mobile agent guide](https://termius.com/blog/8-tips-for-using-ai-agents-on-mobile-in-termius).
Leaving the startup command empty gives you a regular shell instead.

If you use a separate key on the phone, add its public key to the deployment's
`authorized_keys` source file, then recreate the agent using the same Compose
project and files (including the Docker overlay when enabled). The entrypoint
installs that file when the container starts. No password login is enabled.

The default deployment binds SSH to host loopback; the phone needs access through
your existing jump host/tunnel or a reachable `JARA_SSH_BIND` address. `localhost`
on your phone is not the Docker host. Each environment has its own SSH port and
host identity; save a separate Termius host for each.

## Commands after connecting

```sh
jara connect                            # Workspace and running-session menu
jara connect WORKSPACE_ID               # Attach, or start Codex if stopped
jara connect --resume WORKSPACE_ID      # Attach, or start plain codex resume
jara connect --new WORKSPACE_ID         # Additional terminal; warns about shared files
jara connect --worktree WORKSPACE_ID    # New terminal in a separate Git worktree
jara connect --takeover WORKSPACE_ID    # Detach other clients and attach here
```

The selected workspace’s ID appears below the list. Options go before the ID. These commands operate
inside the environment and do not read the laptop's remote configuration or open
another SSH connection. The browser lists workspaces already created by Jara's
clone/copy flow. You can also use ordinary shell commands, Git, editors and tmux
for manually managed folders.

From a computer without the Jara client, open the same menu directly:

```sh
ssh -t -p 2222 agent@example.org jara connect
```

On interactive Linux terminals, the menu adapts to the actual terminal size:

- **1–9:** immediately open the corresponding visible entry; no Enter required.
- **Arrow keys** or **j/k:** move the selection; **Enter** opens it.
- **n/p:** next/previous page. Numbers restart on each page.
- **q** or **Ctrl-C:** leave the browser without stopping running sessions.

Narrow terminals show a project name and status on separate lines. Wider terminals
show them side by side. Names and branches are shortened to fit, long lists are
paged to fit the available height, and resizing/orientation changes redraw the
layout. New clone/copy workspaces remember the local folder name. Older cloned
workspaces derive their name from origin; older copied workspaces show a short ID.
The footer identifies the selected workspace. A `New: PROJECT` entry starts an
additional session. Running entries show whether other clients are attached.

Noninteractive input and `TERM=dumb` retain a numbered, line-based menu: enter the
number followed by Enter. Choose `q` to leave the menu; when run from an
existing shell, this returns to that shell. An empty environment still offers
setup diagnostics. The menu does not clone files from the phone.

Existing terminals are shared by default, so a phone and laptop can see and control
the same session. Use `--takeover` to detach the other viewers. Detach with
**Ctrl-b**, then **d**. Reconnect and run `jara connect` to reattach. A dropped mobile
connection leaves Codex running in tmux. A container restart stops its processes;
workspace files and conversation history persist, and `--resume` opens Codex's
own session selector. Ctrl-C is passed to Codex as usual.

SFTP and editor connections retain ordinary SSH behavior; no forced command or
server-side auto-launch hook is installed. You can use `vim`/`nvim` over SSH when
those packages were selected for the image. Docker and per-project toolchains
work in SSH sessions as documented elsewhere.

## Verification

`scripts/menu_e2e.py` checks 32-, 40- and 80-column SSH layouts, live resizing,
pagination, terminal restoration and creation of a real Codex session at 40 columns.
The standard transport suite covers the menu, direct workspace attachment,
terminal reuse, detach, plain resume and normal shell commands through standard
OpenSSH. The real Codex suite also reattaches to the actual Codex sign-in UI
through plain SSH after an abrupt laptop disconnect, without importing credentials
or making model calls. Termius/iOS/Android rendering and background behavior need
on-device validation; standard OpenSSH testing does not establish those details.
