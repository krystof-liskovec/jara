package jara

// Help for running sessions: listing them everywhere, attaching in place,
// and the per-user daemon that keeps this device's sessions running.
func init() {
	commandHelp["ps"] = helpEntry{
		usage: "jara ps [--remote NAME | --local]",
		summary: "List running Jara sessions: this device's, each remote host's, and those other devices run " +
			"through a remote host. The numbers work with jara attach.",
		options:  []string{"--remote NAME       Only sessions a remote host reaches.", "--local             Only this device's sessions."},
		examples: []string{"jara ps", "jara ps --remote work"},
	}
	commandHelp["attach"] = helpEntry{
		usage: "jara attach [N|ID|NAME|FOLDER] [--remote NAME]",
		summary: "Open a window on a running session where it runs, on this device or on another machine through its remote host. " +
			"Nothing moves: closing the window leaves it running there. Without an argument it picks this folder's session, " +
			"the only one running, or asks. To move a conversation here, open it with jara or use /remote.",
		options:  []string{"--remote NAME       Only consider sessions a remote host reaches."},
		examples: []string{"jara attach", "jara attach 2", "jara attach w-0123456789abcdef", "jara attach ~/src/api"},
	}
	commandHelp["daemon"] = helpEntry{
		usage: "jara daemon install [--no-linger] | uninstall | status | run",
		summary: "A per-user background service (systemd user unit, or a launchd agent on macOS) that runs this device's agent sessions. " +
			"Sessions outlive the terminal; at suspend or shutdown running work moves to the folder's remote host, and work of " +
			"local-only folders continues when the daemon next starts.",
		commands: []string{
			"install [--no-linger]\tInstall and start it. On Linux it also turns on lingering (loginctl enable-linger), so it runs from boot without a login; --no-linger ties it to your logins instead.",
			"uninstall\tStop and remove it.",
			"status\tWhether it runs, its sessions, and interrupted work waiting to continue.",
			"run\tRun it in the foreground (what the service runs).",
		},
		examples: []string{"jara daemon install", "jara daemon status"},
	}
	commandHelp["device"] = helpEntry{
		usage: "jara device allow | deny | status | list [--remote NAME] | open DEVICE [FOLDER] [--remote NAME] [--new]",
		summary: "Let other devices start and join agent sessions on this one, in every folder you have opened with Jara, " +
			"through your remote hosts (the device dials out; it needs the Jara daemon, not an SSH server). Off until you allow it here. " +
			"Anyone who can log in to your hosts can then run agents on this device as you: keep the hosts private, for example on Tailscale.",
		commands: []string{
			"allow | deny\tAccept or refuse calls from other devices.",
			"status\tWhether this device accepts calls, and through which hosts.",
			"list\tReachable devices and their folders.",
			"open DEVICE [FOLDER]\tStart or join a folder's session on another device; FOLDER is its path, name or number.",
		},
		examples: []string{"jara device allow", "jara device list", "jara device open desktop ~/src/api", "jara device open desktop 2 --new"},
	}
	commandHelp["network"] = helpEntry{
		usage: "jara network invite [--remote NAME] [--address HOST[:PORT]] [--minutes N] | devices [--remote NAME] | revoke DEVICE",
		summary: "Invite a device into your network of remote hosts with a one-time code, list the devices that joined, or revoke one. " +
			"invite prints a code for jara join, waits for it, then lets the new device log in to every host.",
		options: []string{
			"--remote NAME       The host the new device joins through (default: the primary).",
			"--address HOST      How other devices reach that host, when your SSH alias or address only works here.",
			"--minutes N         How long the code works (default 15, at most 60).",
		},
		examples: []string{"jara network invite", "jara network invite --address work.tailnet.ts.net:2222", "jara network revoke old-laptop"},
	}
	commandHelp["join"] = helpEntry{
		usage: "jara join CODE [--name NAME]",
		summary: "Join a network with a code from jara network invite: adds its remote hosts here, pinned to their host keys, " +
			"with a new key ~/.ssh/jara_ed25519 they accept.",
		options:  []string{"--name NAME         This device's name in the network (default: its host name)."},
		examples: []string{"jara join jara1.eyJ…"},
	}
	addHelpExamples("root", "jara ps", "jara attach")
}
