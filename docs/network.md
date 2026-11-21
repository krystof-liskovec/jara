# Network of devices

Your remote hosts and your devices form one network. Hosts accept SSH; devices
never need to. A device that allows it is reachable through every host it
knows, so any client of the network can start or join an agent session on it.
A new device joins with a one-time code instead of being set up by hand.

```sh
jara network                 # the whole network: hosts, devices, running sessions
jara network invite          # on a device already in the network: prints a code
jara join jara1.eyJ…         # on the new device: adds every host
jara device allow            # on a device others may run agents on
jara device list             # reachable devices and the folders each has opened
jara device open desktop api # start or join that folder's session on desktop
jara ps                      # running sessions everywhere, devices included
jara attach desktop:/src/api # join one of them
```

**Keep the hosts private.** Anyone who can log in to one of your hosts can
start agents on every device that allows calls, as you and outside any
container. Reach the hosts over a private network such as
[Tailscale](https://tailscale.com) and do not expose their SSH port to the
internet.

## Overview

`jara network` asks every host at once and shows one view: each host with
its rank, address and Jara version (or why it does not answer), every device
the hosts know (accepting calls now and through which hosts, or offline and
when it was last linked; devices that joined with a code are marked), and the
sessions running anywhere, numbered for `jara attach N`. This device comes
first, with what it still needs to accept calls. Devices you added by hand
and never linked are not known to the hosts and do not appear. Colour follows
the terminal's background and is off with `NO_COLOR` or when the output is not
a terminal.

## Devices

`jara device allow` is the device's own, local consent; nothing changes it from
elsewhere, and `jara device deny` withdraws it. While it is allowed and the
[Jara daemon](local-runner.md#background-daemon) runs, the daemon keeps one
outbound SSH connection to each configured remote host:

```text
 desktop (allows calls)             host                       laptop
 ──────────────────────             ────                       ──────
 Jara daemon ── ssh -R ───────────▶ devices/<key>.sock ◀────── jara device open desktop api
   remote.sock                      jara _device-link            (ssh host jara _device-open)
     └─ starts or joins the folder's session, like jara in that folder
```

- The forwarded socket answers only two requests: list the folders this device
  has opened with Jara (every folder with a Jara session or attached to a
  remote), and start or join the session of one of them. The daemon's own
  control socket is never forwarded.
- Opening a folder does exactly what `jara` in it would. A local-only folder
  starts its session here. An attached folder claims its workspace from its
  host first, catching up on the newest checkpoint, then runs here. A
  conversation another machine is busy with is not taken: join it there.
- Sessions other devices start run with the environment of the last Jara
  command you ran in a shell on the device (saved by `jara device allow` and
  by every session you start), so they find the same CLIs, credentials and
  SSH agent.
- Hosts list a device while its link heartbeats (every few seconds; it is
  offline after 30 seconds of silence). A device linked to several hosts is
  listed once.
- A device that allows calls but has no daemon running is not reachable.

## Joining

The code carries a secret and the first host's address and SSH host keys:

1. `jara network invite` derives an SSH key pair from the secret and
   authorizes its public half on the host for at most an hour (15 minutes by
   default), restricted to the single command `jara _join`. The registry it
   leaves there lists every host you rank, with its real address (not your
   SSH alias) and host keys. Pass `--address` when you reach the first host
   at an address other devices cannot, such as `localhost` through a tunnel;
   hosts you reach that way otherwise are left out.
2. `jara join CODE` derives the same key, connects with the host keys pinned,
   and sends this device's name and public key (`~/.ssh/jara_ed25519`,
   created if missing). The host deletes the invitation before anything else,
   so the code works once, authorizes the key, and answers with the registry.
3. The inviting device, still waiting, authorizes the new key on every other
   host. The new device adds the hosts as remotes, in the same rank order,
   pinned to their host keys in `~/.local/share/jara/known_hosts`, and checks
   each lets it in. Remotes it already has under the same name stay as they
   are.

4. Finally it offers to sign the agent CLIs in, so the device can run agents
   itself as well as use the hosts: Codex on this device and on any host
   not signed in yet, each with its own device code (`jara auth codex --all`),
   and Claude Code on this device only, in the browser or with a token from
   `claude setup-token`, so the hosts keep their accounts. Without a terminal,
   or with `--no-sign-in`, it prints those commands instead.

A device joining under a name another device already uses gets a numbered
name (`laptop-2`). `jara network devices` lists the devices a host accepts and
`jara network revoke NAME` removes one from every host. Deployment admin
access (for upgrades) is not shared: set it up with `jara setup` on the
devices that need it.

How hosts accept the keys: container deployments ask `jara _peer-keys`, the
`AuthorizedKeysCommand` that also serves standby peers; other hosts get the
lines in Jara's managed block of `~/.ssh/authorized_keys`. A joined device's
key logs in like yours. Because the agent user owns that list, an agent on a
host could add a login key for itself; see the
[security review](security-review.md). Hosts must run a Jara version with
network support (`jara deployment upgrade`). An `sshd` that uses host keys
other than `/etc/ssh/ssh_host_*_key` can name them with `JARA_HOST_KEY_FILES`
(a glob of public keys) in the environment of Jara on that host.
