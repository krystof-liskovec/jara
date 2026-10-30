#!/usr/bin/env python3
"""Disposable tests for startup path attacks; no secrets/model calls.
Build jara-agent:security-validation before running.
"""
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import time
import uuid

from real_codex_e2e import ROOT, run


def main():
    name = "jara-security-" + uuid.uuid4().hex[:8]
    with tempfile.TemporaryDirectory(prefix="jara-security-") as tmp:
        key = Path(tmp) / "key"
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key))
        container = None
        try:
            container = run("docker", "run", "-d", "--name", name, "--security-opt",
                            f"seccomp={ROOT/'deploy/seccomp/codex.json'}", "-v",
                            f"{key}.pub:/run/jara/authorized_keys:ro",
                            os.environ.get("JARA_TEST_IMAGE", "jara-agent:security-validation"))
            for attempt in range(60):
                try:
                    run("docker", "exec", container, "test", "-s", "/etc/ssh/authorized_keys/agent")
                    break
                except RuntimeError:
                    if attempt == 59: raise
                    time.sleep(.1)
            run("docker", "exec", "-u", "agent", container, "rm", "-rf", "/home/agent/.ssh")
            run("docker", "exec", "-u", "agent", container, "ln", "-s", "/usr/local/bin", "/home/agent/.ssh")
            run("docker", "restart", container)
            assert run("docker", "exec", container, "stat", "-c", "%u:%g", "/usr/local/bin") == "0:0"
            result = subprocess.run(["docker", "exec", "-u", "agent", container, "touch", "/usr/local/bin/jara-attack"], capture_output=True)
            assert result.returncode != 0, "agent gained write access to root-owned executables"
            assert run("docker", "exec", container, "sshd", "-T").find("authorizedkeysfile /etc/ssh/authorized_keys/%u") >= 0
            print("PASS .ssh symlink cannot change root executable ownership; sshd reads root-owned authorized keys", flush=True)

            run("docker", "exec", container, "sh", "-ec", "printf protected > /var/tmp/root-sentinel; chmod 600 /var/tmp/root-sentinel")
            run("docker", "exec", "-u", "agent", container, "rm", "-rf", "/tmp/jara-cache")
            run("docker", "exec", "-u", "agent", container, "ln", "-s", "/var/tmp/root-sentinel", "/tmp/jara-cache")
            print(run("docker", "exec", container, "ls", "-ld", "/tmp/jara-cache", "/var/tmp/root-sentinel"), flush=True)
            run("docker", "restart", container)
            deadline = time.monotonic() + 10
            logs = ""
            while time.monotonic() < deadline:
                logs = run("docker", "logs", container)
                state = run("docker", "inspect", "--format", "{{.State.Running}}", container)
                if state == "false": break
                time.sleep(.1)
            exit_code = run("docker", "inspect", "--format", "{{.State.ExitCode}}", container)
            assert state == "false" and exit_code != "0", (logs, state, exit_code)
            if logs:
                assert "Unsafe /tmp/jara-cache symlink" in logs, logs
            archive = subprocess.run(["docker", "export", container], check=True, capture_output=True).stdout
            with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
                sentinel = tar.getmember("var/tmp/root-sentinel")
                assert sentinel.uid == 0 and sentinel.mode & 0o777 == 0o600
                assert tar.extractfile(sentinel).read() == b"protected"
                cache_link = tar.getmember("tmp/jara-cache")
                assert cache_link.issym() and cache_link.linkname == "/var/tmp/root-sentinel"
            print("PASS cache symlink fails closed without modifying or chowning its target", flush=True)
        finally:
            if container:
                subprocess.run(["docker", "rm", "-fv", container], capture_output=True, timeout=60)


if __name__ == "__main__":
    main()
