#!/usr/bin/env python3
"""Disposable Docker/SSH smoke test. No API calls or personal credentials.

Build first: docker build -f deploy/Dockerfile --build-arg CODEX_VERSION=0.166.0 -t jara-agent:validation .
Run: python3 scripts/e2e.py
"""
import fcntl
import struct
import termios
import base64
import json
import os
from pathlib import Path
import pty
import select
import signal
import socket
import subprocess
import sys
import tempfile
import time
import uuid
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread

ROOT = Path(__file__).resolve().parents[1]


def run(*args, **kwargs):
    result=subprocess.run(args, text=True, capture_output=True, timeout=30, **kwargs)
    if result.returncode:
        raise RuntimeError(f"{args!r}: {result.stdout}\n{result.stderr}")
    return result.stdout.strip()


def main():
    run("go", "build", "-o", str(ROOT / "bin/jara"), "./cmd/jara", cwd=ROOT)
    with tempfile.TemporaryDirectory(prefix="jara-e2e-") as directory:
        temp = Path(directory)
        temp.chmod(0o755)  # Disposable fixture repositories are readable by container UID.
        key = temp / "key"
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key))
        container = "jara-e2e-" + uuid.uuid4().hex[:10]
        run("docker", "run", "-d", "--name", container, "-p", "127.0.0.1::22",
            "--security-opt",f"seccomp={ROOT / 'deploy/seccomp/codex.json'}",
            "-v", f"{key}.pub:/run/jara/authorized_keys:ro", "-v",f"{temp}:/fixtures:ro", os.environ.get("JARA_TEST_IMAGE","jara-agent:validation"))
        try:
            binary_path=run("docker","exec",container,"sh","-c","command -v jara")
            run("docker", "cp", str(ROOT / "bin/jara"), f"{container}:{binary_path}")
            port = run("docker", "port", container, "22/tcp").rsplit(":", 1)[1]
            public = run("docker", "exec", container, "cat", "/var/lib/jara/ssh/ssh_host_ed25519_key.pub")
            known = temp / "known_hosts"
            known.write_text(f"[127.0.0.1]:{port} {public}\n")
            ssh_config = temp / "ssh_config"
            ssh_config.write_text(f"Host fixture\n HostName 127.0.0.1\n Port {port}\n User agent\n IdentityFile {key}\n IdentitiesOnly yes\n UserKnownHostsFile {known}\n")
            env = dict(os.environ, JARA_CONFIG=str(temp / "config.json"),
                       JARA_SSH_CONFIG=str(ssh_config), TERM="xterm-256color",HOME=str(temp))
            jara = str(ROOT / "bin/jara")
            doctor = run(jara, "remote", "add", "fixture", "--host", "fixture", env=env)
            assert "protocol 1" in doctor
            mock = temp / "codex"
            mock.write_text('#!/bin/sh\nif [ "$1" = login ]; then exit 1; fi\nif [ "$1" = sandbox ]; then exit 0; fi\nprintf "JARA_TEST_CODEX: %s\\n" "$*"\nexec sleep 3600\n')
            mock.chmod(0o755)
            run("docker", "cp", str(mock), f"{container}:/usr/local/bin/codex")

            def attach(args, cwd, expected="JARA_TEST_CODEX:", argv=None, menu=False):
                master, slave = pty.openpty()
                fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
                process = subprocess.Popen(argv or [jara, *args], cwd=cwd, env=env,
                                           stdin=slave, stdout=slave, stderr=slave)
                os.close(slave)
                output = b""
                try:
                    deadline = time.monotonic() + 15
                    while expected.encode() not in output and time.monotonic() < deadline:
                        ready, _, _ = select.select([master], [], [], 0.2)
                        if ready:
                            try:
                                output += os.read(master, 65536)
                            except OSError:
                                break
                        if menu and b"1-9 open" in output:
                            os.write(master, b"1")
                            menu = False
                    assert expected.encode() in output, output.decode(errors="replace")
                    time.sleep(0.2)
                    os.write(master, b"\x02d")
                    process.wait(timeout=10)
                    assert process.returncode == 0, output.decode(errors="replace")
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.wait()
                    os.close(master)

            workspace = temp / "workspace"
            workspace.mkdir()
            (workspace / "hello").write_text("original\n")
            attach(["--copy"], workspace)
            config = json.loads((temp / "config.json").read_text())
            binding = config["bindings"][str(workspace)]
            remote_path = f"/home/agent/.local/share/jara/workspaces/{binding['workspace']}"
            metadata = json.loads(run("docker", "exec", container, "cat", remote_path + ".json"))
            assert metadata["name"] == workspace.name
            assert run("docker", "exec", container, "cat", remote_path + "/hello") == "original"
            attach([], workspace)  # Reuses the live terminal.
            inventory=run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "list-sessions","-F","#{session_name}|#{@jara_workspace}|#{session_attached}")
            assert len(inventory.splitlines()) == 1, inventory
            # Phone-style SSH: no local Jara process or client configuration needed.
            plain_ssh = ["ssh", "-tt", "-F", str(ssh_config), "fixture"]
            attach([], temp, argv=plain_ssh + ["jara connect"], menu=True)
            attach([], temp, argv=plain_ssh + ["jara connect " + binding["workspace"]])
            assert len(run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "list-sessions").splitlines()) == 1
            assert "Choose a number" in run("ssh", "-T", "-F", str(ssh_config), "fixture", "jara connect", input="q\n")
            run("ssh", "-T", "-F", str(ssh_config), "fixture", "printf shell-ok")
            print("PASS: plain SSH menu/direct workspace, reuse, detach and ordinary shell", flush=True)
            (workspace / "hello").write_text("local\n")
            failure = subprocess.run([jara, "sync", "push"], cwd=workspace, env=env, capture_output=True, text=True)
            assert failure.returncode and "running agent" in failure.stderr
            run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "kill-server")
            run(jara, "sync", "push", cwd=workspace, env=env)
            assert run("docker", "exec", container, "cat", remote_path + "/hello") == "local"
            run("docker", "exec", container, "sh", "-c", f"printf remote > {remote_path}/hello")
            run(jara, "sync", "pull", cwd=workspace, env=env)
            assert (workspace / "hello").read_text() == "remote"
            (workspace / "hello").write_text("conflict-local")
            run("docker", "exec", container, "sh", "-c", f"printf conflict-remote > {remote_path}/hello")
            failure = subprocess.run([jara, "sync", "push"], cwd=workspace, env=env, capture_output=True, text=True)
            assert failure.returncode and "conflicting" in failure.stderr
            run(jara,"sync","push","--force",cwd=workspace,env=env)
            (workspace/"remove-me").write_text("tracked by transfer")
            run(jara,"sync","push",cwd=workspace,env=env)
            (workspace/"remove-me").unlink()
            run(jara,"sync","push",cwd=workspace,env=env)
            assert run("docker","exec",container,"cat",remote_path+"/remove-me")=="tracked by transfer"
            run(jara,"sync","push","--delete",cwd=workspace,env=env)
            run("docker","exec",container,"test","!","-e",remote_path+"/remove-me")
            (workspace/".jaraignore").write_text("hello\n")
            run(jara,"sync","push","--delete",cwd=workspace,env=env)
            assert run("docker","exec",container,"cat",remote_path+"/hello")=="conflict-local"
            attach(["resume"], workspace, "JARA_TEST_CODEX: resume")
            run("docker", "exec", "-u", "agent", container, "tmux", "-L", "jara", "kill-server")
            attach([], temp, "JARA_TEST_CODEX: resume", argv=plain_ssh + ["jara connect --resume " + binding["workspace"]])

            # A repository with a local-only branch/commit must arrive intact.
            origin=temp/"origin.git"
            run("git","init","--bare",str(origin))
            repository=temp/"repository"
            run("git","clone",str(origin),str(repository))
            run("git","-C",str(repository),"config","user.name","Jara Test")
            run("git","-C",str(repository),"config","user.email","jara@example.invalid")
            (repository/"committed").write_text("base")
            run("git","-C",str(repository),"add","committed")
            run("git","-C",str(repository),"commit","-m","base")
            run("git","-C",str(repository),"push","origin","HEAD")
            run("git","-C",str(repository),"switch","-c","local-only")
            (repository/"committed").write_text("unpublished")
            run("git","-C",str(repository),"commit","-am","unpublished")
            head=run("git","-C",str(repository),"rev-parse","HEAD")
            run("git","-C",str(repository),"remote","set-url","origin","file:///fixtures/origin.git")
            run("docker","exec","-u","agent",container,"git","config","--global","--add","safe.directory","/fixtures/origin.git")
            attach(["--clone"],repository)
            latest=json.loads((temp/"config.json").read_text())
            repo_id=latest["bindings"][str(repository)]["workspace"]
            repo_path=f"/home/agent/.local/share/jara/workspaces/{repo_id}"
            assert run("docker","exec","-u","agent",container,"git","-C",repo_path,"rev-parse","HEAD")==head
            assert run("docker","exec","-u","agent",container,"git","-C",repo_path,"branch","--show-current")=="local-only"
            run("docker","exec","-u","agent",container,"tmux","-L","jara","kill-server")
            (repository/"committed").write_text("working edit")
            run(jara,"sync","push",cwd=repository,env=env)
            assert run("docker","exec",container,"cat",repo_path+"/committed")=="working edit"

            # Repository choice wins over a newly selected global default.
            run(jara,"remote","add","other","--host","fixture",env=env)
            run(jara,"remote","use","other",env=env)
            attach([],repository)
            latest=json.loads((temp/"config.json").read_text())
            assert latest["bindings"][str(repository)]["remote"]=="fixture"

            # Literal editor arguments and generated SSH config, without launching a GUI.
            editor_script=temp/"editor.py"
            editor_result=temp/"editor-result.json"
            editor_script.write_text("import json,sys\nfrom pathlib import Path\nPath("+repr(str(editor_result))+").write_text(json.dumps(sys.argv[1:]))\n")
            adapter=json.dumps([sys.executable,str(editor_script),"{ssh_alias}","{remote_path}"])
            run(jara,"config","set","editor-command","fixture-editor",adapter,env=env)
            run(jara,"config","set","editor","fixture-editor",env=env)
            run(jara,"edit",cwd=repository,env=env)
            assert json.loads(editor_result.read_text())==["jara-fixture",repo_path]
            assert "hostname 127.0.0.1" in (temp/".ssh/jara_config").read_text()

            if subprocess.run(["docker","exec",container,"sh","-c","command -v nvim"],capture_output=True).returncode==0:
                nvim_config=temp/"nvim-config"
                nvim_config.mkdir()
                (nvim_config/"init.lua").write_text("-- imported fixture\n")
                run(jara,"editor","import","nvim","--source",str(nvim_config),"--remote","fixture",env=env)
                assert run("docker","exec",container,"cat","/home/agent/.config/nvim/init.lua")=="-- imported fixture"

            # Real TCP connections verify both forwarding directions.
            def free_port():
                with socket.socket() as sock:
                    sock.bind(("127.0.0.1",0))
                    return sock.getsockname()[1]

            def tunnel(args,check):
                process=subprocess.Popen([jara,*args],env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
                try:
                    deadline=time.monotonic()+10
                    while True:
                        try:
                            check()
                            break
                        except Exception:
                            if time.monotonic()>deadline or process.poll() is not None:
                                raise
                            time.sleep(0.1)
                finally:
                    os.killpg(process.pid,signal.SIGTERM)
                    process.wait(timeout=5)

            run("docker","exec","-d","-u","agent",container,"node","-e","require('http').createServer((q,s)=>s.end('container')).listen(8888,'127.0.0.1')")
            local_port=free_port()
            def check_forward():
                with urllib.request.urlopen(f"http://127.0.0.1:{local_port}",timeout=1) as response:
                    assert response.read()==b"container"
            tunnel(["forward",str(local_port),"--target","127.0.0.1:8888"],check_forward)

            class Handler(BaseHTTPRequestHandler):
                def do_GET(self):
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(b"laptop")
                def log_message(self,*args):
                    pass
            server=HTTPServer(("127.0.0.1",0),Handler)
            Thread(target=server.serve_forever,daemon=True).start()
            try:
                def check_reverse():
                    assert run("docker","exec",container,"node","-e","fetch('http://127.0.0.1:8889').then(r=>r.text()).then(console.log)")=="laptop"
                tunnel(["reverse","8889","--target",f"127.0.0.1:{server.server_port}"],check_reverse)
            finally:
                server.shutdown()
                server.server_close()

            # An explicit remote switches this workspace's remembered container.
            second=container+"-second"
            run("docker","run","-d","--name",second,"-p","127.0.0.1::22","--security-opt",f"seccomp={ROOT / 'deploy/seccomp/codex.json'}","-v",f"{key}.pub:/run/jara/authorized_keys:ro",os.environ.get("JARA_TEST_IMAGE","jara-agent:validation"))
            try:
                second_binary=run("docker","exec",second,"sh","-c","command -v jara")
                run("docker","cp",str(ROOT/"bin/jara"),f"{second}:{second_binary}")
                run("docker","cp",str(mock),f"{second}:/usr/local/bin/codex")
                second_port=run("docker","port",second,"22/tcp").rsplit(":",1)[1]
                second_public=run("docker","exec",second,"cat","/var/lib/jara/ssh/ssh_host_ed25519_key.pub")
                with known.open("a") as file:
                    file.write(f"[127.0.0.1]:{second_port} {second_public}\n")
                with ssh_config.open("a") as file:
                    file.write(f"Host fixture-second\n HostName 127.0.0.1\n Port {second_port}\n User agent\n IdentityFile {key}\n IdentitiesOnly yes\n UserKnownHostsFile {known}\n")
                run(jara,"remote","add","second","--host","fixture-second",env=env)
                attach(["--copy","--remote","second"],workspace)
                attach([],workspace)
                latest=json.loads((temp/"config.json").read_text())
                assert latest["bindings"][str(workspace)]["remote"]=="second"
                assert len(run("docker","exec","-u","agent",second,"tmux","-L","jara","list-sessions").splitlines())==1
            finally:
                subprocess.run(["docker","rm","-f",second],check=True,capture_output=True)

            # Native migration must fail explicitly without modifying anything.
            failure = subprocess.run([jara, "migrate"], env=env, capture_output=True, text=True)
            assert failure.returncode and "unavailable" in failure.stderr
            run("ssh", "-F", str(ssh_config), "fixture", "true")  # Ordinary exec stays available.
            print("PASS: named/multiple remotes, remembered selection, copy/reopen, detach/reattach, busy guard, push/pull, conflicts, explicit deletions, ignore protection, plain resume, unpublished clone and edits, editor adapters/import, forward/reverse TCP, ordinary SSH")
        finally:
            subprocess.run(["docker", "rm", "-f", container], check=True, capture_output=True)


if __name__ == "__main__":
    main()
