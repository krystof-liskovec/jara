#!/usr/bin/env python3
"""Responsive browser over real SSH PTYs; no credentials or model calls.
Build jara-agent:responsive-validation first, or set JARA_TEST_IMAGE.
"""
import fcntl
import json
import os
from pathlib import Path
import re
import signal
import struct
import tempfile
import termios
import time
import uuid
from real_codex_e2e import ROOT, Terminal, run

ESC = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")


def main():
    project = "jara-menu-" + uuid.uuid4().hex[:8]
    with tempfile.TemporaryDirectory(prefix="jara-menu-") as directory:
        temp = Path(directory)
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(temp / "key"))
        image = os.environ.get("JARA_TEST_IMAGE", "jara-agent:responsive-validation")
        terminal = None
        try:
            container = run("docker", "run", "-d", "--name", project, "-p", "127.0.0.1::22", "--security-opt", f"seccomp={ROOT/'deploy/seccomp/codex.json'}", "-v", f"{temp}/key.pub:/run/jara/authorized_keys:ro", image)
            port = run("docker", "port", container, "22/tcp").rsplit(":", 1)[1]
            for attempt in range(50):
                try:
                    public = run("docker", "exec", container, "cat", "/var/lib/jara/ssh/ssh_host_ed25519_key.pub")
                    break
                except RuntimeError:
                    if attempt == 49: raise
                    time.sleep(.1)
            (temp / "known_hosts").write_text(f"[127.0.0.1]:{port} {public}\n")
            ssh = ["ssh", "-tt", "-o", "IdentitiesOnly=yes", "-o", f"UserKnownHostsFile={temp/'known_hosts'}", "-i", str(temp / "key"), "-p", port, "agent@127.0.0.1"]
            # Host Python constructs fixture JSON; the agent needs no Python runtime.
            fixture = temp / "workspaces"
            fixture.mkdir()
            for i in range(16):
                workspace = f"w-{i:02d}"
                path = f"/home/agent/.local/share/jara/workspaces/{workspace}"
                (fixture / workspace).mkdir()
                (fixture / f"{workspace}.json").write_text(json.dumps(dict(id=workspace, name=f"project-{i:02d}-with-a-long-descriptive-name", path=path, branch="feature/long-branch-name", ready=True)))
            run("docker", "exec", "-u", "agent", container, "mkdir", "-p", "/home/agent/.local/share/jara")
            run("docker", "cp", str(fixture), f"{container}:/home/agent/.local/share/jara/workspaces")
            run("docker", "exec", container, "chown", "-R", "agent:agent", "/home/agent/.local/share/jara")
            for i in range(2):
                run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "new-session", "-d", "-s", f"fixture-{i}", "sleep", "3600")
                run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "set-option", "-t", f"fixture-{i}", "@jara_workspace", f"w-{i:02d}")
            env = dict(os.environ, TERM="xterm-256color")
            wrapper = 'before=$(stty -g); jara connect; after=$(stty -g); test "$before" = "$after" && echo MENU-RESTORED'

            def resize(cols, rows):
                fcntl.ioctl(terminal.master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
                os.kill(terminal.process.pid, signal.SIGWINCH)

            def frame():
                terminal.drain()
                text = terminal.output.decode(errors="replace")
                return ESC.sub("", text.rsplit("\x1b[H\x1b[2J", 1)[-1]).replace("\r", "")

            def wait(text, timeout=15):
                deadline = time.monotonic() + timeout
                while time.monotonic() < deadline:
                    screen = frame()
                    if text in screen: return screen
                    if terminal.process.poll() is not None: break
                    time.sleep(.05)
                raise AssertionError(f"missing {text!r}: {terminal.output.decode(errors='replace')}")

            def bounds(screen, cols, rows):
                lines = screen.splitlines()
                assert len(lines) <= rows, (cols, rows, lines)
                assert all(len(line) <= cols - 1 for line in lines), (cols, rows, lines)

            def snapshot(name):
                if os.environ.get("JARA_MENU_SNAPSHOT_DIR"):
                    target = Path(os.environ["JARA_MENU_SNAPSHOT_DIR"])
                    target.mkdir(parents=True, exist_ok=True)
                    (target / (name + ".txt")).write_text(frame())

            terminal = Terminal(ssh + [wrapper], temp, env)
            resize(40, 18)
            screen = wait("1–6 of 35")
            wait("1-9 open")
            bounds(frame(), 40, 18)
            snapshot("phone-40")
            os.write(terminal.master, b"n")
            bounds(wait("7–12 of 35"), 40, 18)
            os.write(terminal.master, b"p")
            wait("1–6 of 35")
            os.write(terminal.master, b"\x1b[B")
            wait("> 2")
            resize(80, 14)
            bounds(wait("1–9 of 35"), 80, 14)
            snapshot("desktop-80")
            resize(32, 10)
            bounds(wait("1–2 of 35"), 32, 10)
            snapshot("phone-32")
            resize(40, 4)
            bounds(wait("Enlarge terminal"), 40, 4)
            resize(40, 18)
            wait("1–6 of 35")
            os.write(terminal.master, b"q")
            wait("MENU-RESTORED")
            terminal.process.wait(timeout=10)
            assert terminal.process.returncode == 0
            terminal.close(); terminal = None
            print("PASS: 32/40/80-column SSH layout, live resize, short-screen recovery, pagination, arrow keys and restored terminal", flush=True)

            terminal = Terminal(ssh + [wrapper], temp, env)
            resize(40, 18)
            wait("1–6 of 35")
            os.write(terminal.master, b"\x03")
            wait("MENU-RESTORED")
            terminal.process.wait(timeout=10)
            terminal.close(); terminal = None
            print("PASS: Ctrl-C exits the browser and restores the shell terminal", flush=True)

            terminal = Terminal(ssh + ["jara connect"], temp, env)
            resize(40, 18)
            wait("1–6 of 35")
            os.write(terminal.master, b"n")
            wait("7–12 of 35")
            os.write(terminal.master, b"2")  # New session in project-02, without an Enter key.
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                terminal.drain()
                inventory = run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "list-sessions", "-F", "#{session_name}|#{@jara_workspace}")
                target = next((line.split("|")[0] for line in inventory.splitlines() if line.endswith("|w-02")), None)
                if target:
                    screen = run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "capture-pane", "-p", "-t", target)
                    if "Codex" in screen: break
                time.sleep(.1)
            else: raise AssertionError(terminal.output.decode(errors="replace"))
            assert len(inventory.splitlines()) == 3, inventory
            terminal.close(abrupt=True); terminal = None
            terminal = Terminal(ssh + ["jara connect w-02"], temp, env)
            resize(40, 18)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                terminal.drain()
                attached = run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "list-sessions", "-F", "#{@jara_workspace}|#{session_attached}")
                if "w-02|1" in attached: break
                time.sleep(.1)
            assert "w-02|1" in attached, attached
            terminal.close(); terminal = None
            print("PASS: single-key new session starts actual Codex on a 40-column SSH terminal; disconnect and reattach preserve it", flush=True)
        finally:
            if terminal: terminal.close(abrupt=True)
            run("docker", "rm", "-fv", project)


if __name__ == "__main__":
    main()
