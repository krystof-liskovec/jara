#!/usr/bin/env python3
"""Compose + real Codex terminal test. Never imports personal credentials.

Build jara-agent:real-validation from the current tree before running.
"""
import fcntl
import argparse
import json
import os
from pathlib import Path
import pty
import select
import signal
import socket
import struct
import subprocess
import tempfile
import termios
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]


def run(*args, timeout=30, **kwargs):
    result = subprocess.run(args, text=True, capture_output=True, timeout=timeout, **kwargs)
    if result.returncode:
        raise RuntimeError(f"{args!r}: {result.stdout}\n{result.stderr}")
    return result.stdout.strip()


class Terminal:
    def __init__(self, argv, cwd, env):
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
        self.process = subprocess.Popen(argv, cwd=cwd, env=env, stdin=slave,
                                        stdout=slave, stderr=slave, start_new_session=True)
        os.close(slave)
        self.output = b""
        self.color_answered = False

    def drain(self):
        while select.select([self.master], [], [], 0)[0]:
            try:
                data = os.read(self.master, 65536)
            except OSError:
                break
            if not data:
                break
            self.output += data
            if not self.color_answered and b"\x1b]11;?\x1b\\" in self.output:
                os.write(self.master, b"\x1b]11;rgb:1212/3434/5656\x1b\\")
                self.color_answered = True
            if b"\x1b[6n" in data:
                os.write(self.master, b"\x1b[1;1R")
            if b"\x1b[c" in data:
                os.write(self.master, b"\x1b[?1;2c")
            if b"\x1b[>c" in data:
                os.write(self.master, b"\x1b[>0;1;0c")

    def send(self, data):
        os.write(self.master, data)

    def close(self, abrupt=False):
        if self.process.poll() is None:
            if abrupt:
                os.killpg(self.process.pid, signal.SIGKILL)
            else:
                self.send(b"\x02")
                time.sleep(0.1)
                self.send(b"d")
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait(timeout=5)
                os.close(self.master)
                raise AssertionError("Ctrl-b d did not detach the real terminal")
        os.close(self.master)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--live-auth",type=Path,help="Explicitly authorized login cache to copy into this disposable container")
    args=parser.parse_args()
    auth=args.live_auth.resolve() if args.live_auth else None
    run("go", "build", "-o", str(ROOT / "bin/jara"), "./cmd/jara", cwd=ROOT)
    with tempfile.TemporaryDirectory(prefix="jara-real-") as directory:
        temp = Path(directory)
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(temp / "key"))
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        project = "jara-real-" + uuid.uuid4().hex[:10]
        override = temp / "compose.yaml"
        override.write_text(json.dumps({"services": {"agent": {"image": os.environ.get("JARA_TEST_IMAGE", "jara-agent:real-validation")}}}))
        env = dict(os.environ, CODEX_VERSION="0.166.0", JARA_SSH_BIND="127.0.0.1",
                   JARA_SSH_PORT=str(port), AUTHORIZED_KEYS_FILE=str(temp / "key.pub"),
                   GITLAB_TOKEN_FILE="/dev/null", GITEA_TOKEN_FILE="/dev/null")
        compose = ["docker", "compose", "-p", project, "-f", str(ROOT / "deploy/compose.yaml"), "-f", str(override)]
        terminal = None
        try:
            run(*compose, "up", "-d", "--no-build", env=env)
            container = run(*compose, "ps", "-q", "agent", env=env)
            deadline = time.monotonic() + 20
            while True:
                try:
                    public = run("docker", "exec", container, "cat", "/var/lib/jara/ssh/ssh_host_ed25519_key.pub")
                    break
                except RuntimeError:
                    if time.monotonic() > deadline:
                        raise
                    time.sleep(0.1)
            (temp / "known_hosts").write_text(f"[127.0.0.1]:{port} {public}\n")
            (temp / "ssh_config").write_text(f"Host real\n HostName 127.0.0.1\n Port {port}\n User agent\n IdentityFile {temp}/key\n IdentitiesOnly yes\n UserKnownHostsFile {temp}/known_hosts\n")
            client_env = dict(env, HOME=str(temp), JARA_CONFIG=str(temp / "config.json"),
                              JARA_SSH_CONFIG=str(temp / "ssh_config"), TERM="xterm-256color")
            # These are intentionally not passed to the container.
            for name in ("OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "CODEX_HOME"):
                client_env.pop(name, None)
            jara = str(ROOT / "bin/jara")
            print(run(jara, "remote", "add", "real", "--host", "real", env=client_env), flush=True)
            workspace = temp / "workspace"
            workspace.mkdir()
            (workspace / "hello.txt").write_text("persistent workspace\n")
            terminal = Terminal([jara, "--copy"], workspace, client_env)

            def inventory():
                return run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "list-sessions", "-F", "#{session_name}|#{session_id}|#{session_attached}")

            def screen():
                result=subprocess.run(["docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "capture-pane", "-p", "-S", "-100"],text=True,capture_output=True,timeout=10)
                if result.returncode and "no server running" not in result.stderr and "No such file" not in result.stderr:
                    raise RuntimeError(result.stderr)
                return result.stdout.strip()

            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                terminal.drain()
                try:
                    text = screen()
                    if "Codex" in text:
                        break
                except RuntimeError:
                    pass
                if terminal.process.poll() is not None:
                    raise AssertionError(terminal.output.decode(errors="replace"))
                time.sleep(0.1)
            else:
                raise AssertionError("Real Codex UI did not start: " + terminal.output.decode(errors="replace"))
            assert "Choose how you want to use Codex" in text,text
            print("PASS: real Codex sign-in UI over Compose/OpenSSH/Jara/tmux",flush=True)
            initial=inventory().split("|")[:2]
            terminal.close(abrupt=True)
            terminal=None
            assert inventory().split("|")[:2]==initial
            binding=json.loads((temp/"config.json").read_text())["bindings"][str(workspace)]
            terminal=Terminal(["ssh", "-tt", "-F", str(temp/"ssh_config"), "real", "jara connect " + binding["workspace"]],workspace,client_env)
            deadline=time.monotonic()+10
            while time.monotonic()<deadline:
                terminal.drain()
                if inventory().endswith("|1"):
                    break
                time.sleep(0.1)
            assert inventory().split("|")[:2]==initial
            assert "Choose how you want to use Codex" in screen()
            terminal.close()
            terminal=None
            print("PASS: abrupt SSH loss and plain SSH jara connect reattach preserve the real Codex terminal",flush=True)

            binding=json.loads((temp/"config.json").read_text())["bindings"][str(workspace)]
            remote_path="/home/agent/.local/share/jara/workspaces/"+binding["workspace"]
            # Exercise the real Codex sandbox without a model call, including its boundary.
            sandbox_script="printf sandbox-ok > sandbox-check.txt; if printf forbidden > /home/agent/outside-sandbox.txt 2>/dev/null; then exit 73; fi"
            run("docker","exec","-u","agent","-w",remote_path,container,"codex","sandbox","-c","sandbox_mode=\"workspace-write\"","--","/bin/sh","-c",sandbox_script)
            assert run("docker","exec",container,"cat",remote_path+"/sandbox-check.txt")=="sandbox-ok"
            run("docker","exec",container,"test","!","-e","/home/agent/outside-sandbox.txt")
            print("PASS: real Codex sandbox writes inside workspace and refuses writes outside",flush=True)

            def recreate():
                nonlocal container
                before=run("docker","exec",container,"cat","/var/lib/jara/ssh/ssh_host_ed25519_key.pub")
                run(*compose,"up","-d","--no-build","--force-recreate",env=env)
                container=run(*compose,"ps","-q","agent",env=env)
                deadline=time.monotonic()+15
                while time.monotonic()<deadline:
                    check=subprocess.run(["ssh","-F",str(temp/"ssh_config"),"-o","BatchMode=yes","real","true"],capture_output=True,timeout=10)
                    if check.returncode==0:
                        break
                    time.sleep(0.1)
                assert check.returncode==0,"SSH did not become ready after recreation"
                assert run("docker","exec",container,"cat","/var/lib/jara/ssh/ssh_host_ed25519_key.pub")==before
                assert run("docker","exec",container,"cat",remote_path+"/hello.txt")=="persistent workspace"

            recreate()
            print("PASS: Compose recreation preserves workspace, metadata and SSH host identity",flush=True)

            if auth:
                run("docker","cp",str(auth),f"{container}:/home/agent/.codex/auth.json")
                run("docker","exec",container,"chown","agent:agent","/home/agent/.codex/auth.json")
                run("docker","exec",container,"chmod","600","/home/agent/.codex/auth.json")
                status=run("docker","exec","-u","agent",container,"codex","login","status")
                print("PASS: authorized login cache accepted by real remote Codex",flush=True)
                probe=subprocess.run(["docker","exec","-u","agent",container,"tmux","-L","jara","has-session"],capture_output=True)
                if probe.returncode==0:
                    run("docker","exec","-u","agent",container,"tmux","-L","jara","kill-server")
                config='[projects.'+json.dumps(remote_path)+']\ntrust_level = "trusted"\n'
                run("docker","exec","-i","-u","agent",container,"sh","-c","cat > /home/agent/.codex/config.toml",input=config)
                terminal=Terminal([jara],workspace,client_env)
                deadline=time.monotonic()+30
                while time.monotonic()<deadline:
                    terminal.drain()
                    text=screen()
                    if "›" in text:
                        break
                    if terminal.process.poll() is not None:
                        raise AssertionError(terminal.output.decode(errors="replace"))
                    time.sleep(0.1)
                else:
                    raise AssertionError("Signed-in Codex did not reach prompt:\n"+text)
                print("PASS: signed-in real Codex reached its interactive prompt",flush=True)
                prompt="Create a file named jara-live-check.txt containing exactly jara-live-ok followed by a newline. Do not modify any other files. Then reply JARA_LIVE_OK."
                terminal.send(b"\x1b[200~"+prompt.encode()+b"\x1b[201~")
                time.sleep(0.3)
                terminal.send(b"\r")
                deadline=time.monotonic()+90
                while time.monotonic()<deadline:
                    terminal.drain()
                    text=screen()
                    # Tool approval is allowed only for the requested fixture file.
                    if "Would you like to" in text and "jara-live-check.txt" in text:
                        terminal.send(b"\r")
                    result=subprocess.run(["docker","exec",container,"cat",remote_path+"/jara-live-check.txt"],text=True,capture_output=True)
                    if result.returncode==0 and result.stdout=="jara-live-ok\n" and "JARA_LIVE_OK" in text and "esc to interrupt" not in text:
                        break
                    if "Reconnecting" in text or "error" in text.lower():
                        pass
                    time.sleep(0.2)
                else:
                    raise AssertionError("Live Codex turn did not complete:\n"+text)
                print("PASS: real authenticated Codex turn created the exact requested file",flush=True)
                live_session=inventory().split("|")[:2]
                terminal.close()
                terminal=None
                terminal=Terminal([jara],workspace,client_env)
                deadline=time.monotonic()+10
                while time.monotonic()<deadline:
                    terminal.drain()
                    if "JARA_LIVE_OK" in screen() and inventory().endswith("|1"):
                        break
                    time.sleep(0.1)
                assert inventory().split("|")[:2]==live_session
                assert "JARA_LIVE_OK" in screen()
                terminal.close()
                terminal=None
                print("PASS: completed live conversation remains visible after reattachment",flush=True)

                recreate()
                assert run("docker","exec",container,"cat",remote_path+"/jara-live-check.txt")=="jara-live-ok"
                terminal=Terminal([jara,"resume"],workspace,client_env)
                deadline=time.monotonic()+20
                while time.monotonic()<deadline:
                    terminal.drain()
                    text=screen()
                    if "Resume a previous session" in text and "1 / 1" in text:
                        break
                    if terminal.process.poll() is not None:
                        raise AssertionError(terminal.output.decode(errors="replace"))
                    time.sleep(0.1)
                else:
                    raise AssertionError("Plain resume picker did not show saved live conversation:\n"+text)
                terminal.send(b"\r")
                deadline=time.monotonic()+15
                while time.monotonic()<deadline:
                    terminal.drain()
                    if "JARA_LIVE_OK" in screen():
                        break
                    time.sleep(0.1)
                assert "JARA_LIVE_OK" in screen(),"Selected saved conversation did not reopen"
                print("PASS: Compose recreation preserves host identity, workspace, and real Codex resume picker",flush=True)
        finally:
            try:
                if terminal is not None:
                    terminal.close(abrupt=True)
            finally:
                run(*compose, "down", "-v", "--remove-orphans", env=env)


if __name__ == "__main__":
    main()
