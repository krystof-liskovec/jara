#!/usr/bin/env python3
"""Real rootless DinD + SSH + Codex sandbox; no credentials or model calls.

Build current images first; supply JARA_TEST_IMAGE and JARA_DIND_TEST_IMAGE.
"""
import base64
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import tempfile
import time
import uuid
from real_codex_e2e import ROOT, run


def main():
    run("go", "build", "-o", str(ROOT / "bin/jara"), "./cmd/jara", cwd=ROOT)
    project = "jara-docker-" + uuid.uuid4().hex[:8]
    with tempfile.TemporaryDirectory(prefix="jara-docker-") as directory:
        temp = Path(directory)
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(temp / "key"))
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        override = temp / "images.json"
        override.write_text(json.dumps({"services": {
            "agent": {"image": os.environ.get("JARA_TEST_IMAGE", "jara-dind-validation-agent")},
            "docker": {"image": os.environ.get("JARA_DIND_TEST_IMAGE", "jara-dind-validation-docker")},
        }}))
        env = dict(os.environ, CODEX_VERSION="0.166.0", AUTHORIZED_KEYS_FILE=str(temp / "key.pub"),
                   JARA_SSH_BIND="127.0.0.1", JARA_SSH_PORT=str(port), GITLAB_TOKEN_FILE="/dev/null", GITEA_TOKEN_FILE="/dev/null")
        compose = ["docker", "compose", "-p", project, "-f", str(ROOT / "deploy/compose.yaml"),
                   "-f", str(ROOT / "deploy/compose.docker.yaml"), "-f", str(override)]
        def dc(*args):
            return run(*compose, *args, env=env, timeout=180)
        tunnel = None
        second = None
        try:
            dc("up", "-d", "--no-build", "--wait", "--wait-timeout", "90")
            agent, daemon = dc("ps", "-q", "agent"), dc("ps", "-q", "docker")
            public = run("docker", "exec", agent, "cat", "/var/lib/jara/ssh/ssh_host_ed25519_key.pub")
            (temp / "known_hosts").write_text(f"[127.0.0.1]:{port} {public}\n")
            ssh = ["ssh", "-p", str(port), "-i", str(temp / "key"), "-o", "IdentitiesOnly=yes",
                   "-o", f"UserKnownHostsFile={temp / 'known_hosts'}", "agent@127.0.0.1"]
            def remote(command):
                return run(*ssh, command, timeout=120)
            def report(message):
                print("PASS " + message, flush=True)
            assert remote("id -u") == "1001"
            assert remote("echo $DOCKER_HOST") == "unix:///run/jara-docker/docker.sock"
            assert remote("bash -lc 'echo $DOCKER_HOST'") == "unix:///run/jara-docker/docker.sock"
            assert remote("echo $GOCACHE") == "/tmp/jara-cache/go-build"
            assert "rootless" in remote("docker info --format '{{json .SecurityOptions}}'")
            engine = remote("docker info --format '{{.ID}}'")
            assert engine != run("docker", "info", "--format", "{{.ID}}")
            remote("docker compose version && docker buildx version")
            assert run("docker", "inspect", "--format", "{{.HostConfig.Privileged}}", agent) == "false"
            report("SSH and login shells reach separate rootless daemon; agent unprivileged; plugins installed")
            # Build a tiny image without relying on registry access in the nested daemon.
            remote("mkdir -p /home/agent/docker-test; chmod 700 /home/agent/docker-test")
            run("docker", "cp", f"{daemon}:/bin/busybox", str(temp / "busybox"))
            run("docker", "cp", str(temp / "busybox"), f"{agent}:/home/agent/docker-test/busybox")
            loader = run("docker", "exec", daemon, "sh", "-c", "echo /lib/ld-musl-*.so.1")
            run("docker", "cp", f"{daemon}:{loader}", str(temp / "libc"))
            run("docker", "cp", str(temp / "libc"), f"{agent}:/home/agent/docker-test/libc")
            run("docker", "exec", agent, "chown", "1001:1001", "/home/agent/docker-test/busybox", "/home/agent/docker-test/libc")
            remote(f"cd /home/agent/docker-test; printf 'FROM scratch\nCOPY busybox /bin/busybox\nCOPY libc {loader}\nENTRYPOINT [\"/bin/busybox\"]\n' > Dockerfile; printf bind-ok > private; chmod 600 private")
            # Docker socket APIs bypass the filesystem sandbox; this is intentionally opt-in.
            output = remote("cd /home/agent/docker-test; codex sandbox -c 'sandbox_mode=\"workspace-write\"' -- /bin/sh -ec 'docker build -t jara-fixture .; docker run --rm -v \"$PWD:/work\" jara-fixture sh -c \"/bin/busybox cat /work/private; echo written > /work/result\"; cat result; if (printf forbidden > /home/agent/jara-outside-probe) 2>/dev/null; then exit 73; fi'")
            assert "bind-ok" in output and "written" in output, repr(output)
            report("real Codex sandbox builds and runs image, reads private bind mount, writes agent-owned file")
            service = '''services:\n  web:\n    image: jara-fixture\n    command: [nc, -lk, -p, "8080", -e, /www/respond]\n    ports: ["127.0.0.1:18080:8080"]\n    volumes: ["./:/www:ro", "data:/data"]\nvolumes:\n  data:\n'''
            response = "#!/bin/busybox sh\nprintf 'HTTP/1.1 200 OK\\r\\nContent-Length: 6\\r\\nConnection: close\\r\\n\\r\\n'\n/bin/busybox cat /www/index.html\n"
            remote("cd /home/agent/docker-test; printf %s " + shlex.quote(response) + " > respond; chmod 755 respond")
            remote("cd /home/agent/docker-test; printf %s " + shlex.quote(service) + " > compose.yaml; printf web-ok > index.html; docker compose -p nested up -d")
            for attempt in range(30):
                try:
                    assert remote("curl -fsS --max-time 2 http://127.0.0.1:18080") == "web-ok"
                    break
                except RuntimeError:
                    if attempt == 29:
                        raise
                    time.sleep(.1)
            remote("cd /home/agent/docker-test; docker compose -p nested exec -T web /bin/busybox sh -c 'echo volume-ok > /data/marker'")
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0)); forwarded = sock.getsockname()[1]
            tunnel = subprocess.Popen(ssh[:-1] + ["-o", "ExitOnForwardFailure=yes", "-N", "-L", f"127.0.0.1:{forwarded}:127.0.0.1:18080", ssh[-1]], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            for attempt in range(30):
                result = subprocess.run(["curl", "-fsS", f"http://127.0.0.1:{forwarded}"], capture_output=True, text=True)
                if result.returncode == 0:
                    assert result.stdout == "web-ok"; break
                time.sleep(.1)
            else:
                raise AssertionError("SSH tunnel unavailable")
            report("nested Compose service, named volume, localhost publication and SSH forwarding")
            tunnel.terminate(); tunnel.wait(); tunnel = None
            dc("up", "-d", "--no-deps", "--no-build", "--force-recreate", "--wait", "agent")
            assert remote("docker info --format '{{.ID}}'") == engine
            assert remote("curl -fsS http://127.0.0.1:18080") == "web-ok"
            assert remote("cd /home/agent/docker-test; docker compose -p nested exec -T web /bin/busybox cat /data/marker") == "volume-ok"
            report("agent recreation preserves daemon, running nested service, named volume and SSH identity")
            token = base64.urlsafe_b64encode(json.dumps({"op": "doctor"}).encode()).decode().rstrip("=")
            diagnosis = json.loads(remote("jara _rpc " + token))
            assert diagnosis["OK"] and "OK rootless Docker daemon" in diagnosis["Checks"], diagnosis
            report("Jara doctor validates real sandbox and rootless daemon")
            env_file = temp / "deployment.env"
            env_file.write_text("CODEX_VERSION=0.166.0\n")
            update = [str(ROOT / "bin/jara"), "host", "update", "--project", project, "--compose",
                      str(ROOT / "deploy/compose.yaml"), "--env-file", str(env_file), "--version", "0.166.0", "--check"]
            missing = subprocess.run(update, text=True, capture_output=True, env=env)
            assert missing.returncode and "--compose-extra" in missing.stderr, missing
            assert "is current" in run(*update, "--compose-extra", str(ROOT / "deploy/compose.docker.yaml"), env=env)
            report("host updater diagnoses missing Docker overlay; check with overlay succeeds")
            second = compose.copy()
            second[second.index("-p") + 1] = project + "-other"
            second_env = dict(env, JARA_SSH_PORT="0")
            run(*second, "up", "-d", "--no-build", "--wait", "docker", env=second_env, timeout=120)
            other = run(*second, "ps", "-q", "docker", env=second_env)
            endpoint = "unix:///home/rootless/docker-run/docker.sock"
            assert run("docker", "exec", other, "docker", "-H", endpoint, "info", "--format", "{{.ID}}") != engine
            assert not run("docker", "exec", other, "docker", "-H", endpoint, "images", "-q", "jara-fixture")
            report("second environment has independent daemon and image store")
        finally:
            if tunnel:
                tunnel.terminate(); tunnel.wait()
            dc("down", "--volumes", "--remove-orphans")
            if second:
                run(*second, "down", "--volumes", "--remove-orphans", env=second_env, timeout=60)


if __name__ == "__main__":
    main()
