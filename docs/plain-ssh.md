# Plain SSH and phone access

The environment runs ordinary OpenSSH. Jara is only needed on the server; a phone
or other SSH client does not need the local Jara CLI. Your phone can attach to the
same persistent Codex terminals you use from your laptop.

## Termius setup

1. Add a host with your deployment's reachable hostname, published SSH port
   (2222 by default), the Jara SSH username, and an authorized SSH key. Guided
   setup asks for this username; it defaults to `agent`.
2. Verify the SSH host fingerprint through trusted host access, as described in
   the [deployment guide](../README.md#deploy-on-a-linux-docker-host).
3. Connect and run `jara connect` to browse workspaces and running sessions.

For direct access to the menu whenever you connect, set the host's **Startup
Command** to `jara connect`. Termius documents startup snippets in its
[snippets guide](https://docs.termius.com/terminal/snippets.md).

If your Termius plan does not include startup snippets, use a separate phone
key with `--connect-only`:

```sh
jara key install --remote NAME --public-key ~/.ssh/termius.pub --connect-only
```

Keep the unrestricted key for VS Code. Make the phone key's public half
available to your Jara client and install it with the command above. Configure
Termius to use that key; SSH will launch `jara connect` automatically for it. A
connect-only key cannot open a normal shell, use SFTP, or forward ports.

Guided deployments support `jara key install --remote NAME --public-key FILE`,
`jara key list`, and `jara key remove --fingerprint SHA256:...`. These use the
saved Docker-host connection to update the live authorized key file without
restarting the agent.
For a manual Compose deployment, edit `deploy/authorized_keys` and recreate the
agent using the same Compose project and files (including the Docker overlay when
enabled). The entrypoint installs that file when the container starts. No password
login is enabled.

The default deployment binds SSH to host loopback; the phone needs access through
your existing jump host/tunnel or a reachable `JARA_SSH_BIND` address. `localhost`
on your phone is not the Docker host. Each environment has its own SSH port and
host identity; save a separate Termius host for each.

## Commands after connecting

The SSH examples use `agent`, the default username. Replace it with your selected
Jara SSH username if you changed it during setup.

```sh
jara connect                            # Projects and conversations
jara connect WORKSPACE_ID               # Attach, or start Codex if stopped
jara connect --resume WORKSPACE_ID      # Attach, or start plain codex resume
jara connect --new WORKSPACE_ID         # Additional terminal; warns about shared files
jara connect --worktree WORKSPACE_ID    # New terminal in a separate Git worktree
jara connect --takeover WORKSPACE_ID    # Detach other clients and attach here
```

The menu opens on **Projects**. Choose a project to see its saved conversations,
then select a prompt or conversation name to resume. Conversations with a matched
running terminal show a `live` count. Opening one reattaches to that terminal;
multiple terminals offer a short chooser. Codex's reported active state appears
as `working` when available.

Project terminals and all host terminals have their own entries. They include
older terminals whose conversation cannot be identified. `All Codex conversations`
opens Codex's native picker as a fallback. History comes from the installed
Codex app-server's local metadata API; it does not require an inference request.

From a computer without the Jara client, open the same menu directly:

```sh
ssh -t -p 2222 agent@example.org jara connect
```

On interactive Linux terminals, the menu adapts to the actual terminal size:

- **1–9:** immediately open the corresponding visible entry; no Enter required.
- **Up/Down** or **j/k:** move the selection; **Right** or **Enter** opens it.
- **Left** or **Backspace:** return to the parent menu. Backspace edits an active
  search first; with an empty search it goes back.
- **n/p:** next/previous page. Numbers restart on each page.
- **q:** go back. **Ctrl-C:** close the entire Jara interface from any menu.
  Running terminal sessions are left running.

All menus use the same layout on phone and desktop: a full-width title with one
short metadata line beneath it. Highlighting changes only the selection marker
and color. There are no hover previews or changing side panels. Lists support
search, pagination and resizing. Returning from a project preserves its position
in the project list. The project order follows the latest conversation activity.

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

For Jara's own conversation UI, use
`jara connect --new WORKSPACE_ID` from an ordinary SSH shell.
It uses plain scrolling text and typed commands: `/compose` and `/send` for
multiline prompts, `/status` to check activity, `/sessions` to browse history,
and `/answer` for agent questions. Ctrl-C interrupts a running turn and exits
when idle. See [conversation controls](conversation-interface.md) for approval
and cancellation behavior. Existing terminals retain the UI they started with.

Connections using an ordinary key retain normal SSH behavior, so VS Code and
SFTP continue to work. The connect-only Termius key forces `jara connect` and
cannot run shell commands or SFTP. You can use `vim`/`nvim` over SSH when those
packages were selected for the image. Docker and per-project toolchains work in
SSH sessions as documented elsewhere.

## Working from a phone

The Jara conversation interface adapts to phone screens:

- Below 18 rows (the on-screen keyboard is open) the model, reasoning, and
  context meter move into the input box border, and the hint line is dropped,
  so most of the screen shows the conversation.
- Below 60 columns function-key labels and desktop-only hints disappear. Tap
  instead: the model or reasoning label opens its picker, a menu entry chooses
  it, and the `[ ⏎/y Allow ]` / `[ n Deny ]` row answers an approval; `/diff`
  lists the conversation's changes. With an empty input box, Enter or `y`
  allows the approval and `n` denies it.
- End a line with `\` before Enter to continue the message on a new line.
- Long code and diff lines are cut with `…` instead of wrapping; `/export`
  saves the full text.

**Push notifications.** `/notify https://ntfy.sh/YOUR-SECRET-TOPIC` makes the
session send a notification when a turn that ran for 15 seconds or more
finishes, or an approval or question waits, even after the phone disconnects.
Install the ntfy app and subscribe to the same topic; any webhook that accepts a
POSTed text body works too. Notifications name the workspace and the event,
never conversation text. `/notify test` sends one now and `/notify off` stops
them. The setting lives on the host and needs a persistent Jara session.

**Reconnecting.** `jara connect --last` goes straight back to the most recently
used running session, or opens the menu when none runs; use it as the Termius
Startup Command. For connections that survive network changes, use mosh: the
image includes `mosh-server`, and Compose publishes UDP ports 60001-60004 on
`JARA_SSH_BIND` (change the host side with `JARA_MOSH_PORTS`, e.g.
`61001-61004`). Enable Mosh on the Termius host and allow those UDP ports
through any firewall; a jump-host tunnel carries only SSH, so mosh needs the
ports reachable directly. Connect-only keys run `jara connect` and cannot start
`mosh-server`.

## Verification

`scripts/menu_e2e.py` checks 32-, 40- and 80-column SSH layouts, live resizing,
pagination, terminal restoration and creation of a real Codex session at 40 columns.
The standard transport suite covers the menu, direct workspace attachment,
terminal reuse, detach, plain resume and normal shell commands through standard
OpenSSH. The real Codex suite also reattaches to the actual Codex sign-in UI
through plain SSH after an abrupt laptop disconnect, without importing credentials
or making model calls. Termius/iOS/Android rendering and background behavior need
on-device validation; standard OpenSSH testing does not establish those details.
