#!/usr/bin/env python3
"""Disposable real Docker update from Codex 0.165.0 to 0.166.0; no credentials/inference.

Build first: docker build -f deploy/Dockerfile --build-arg CODEX_VERSION=0.165.0 -t jag-agent:update-from .
"""
import base64
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import uuid

ROOT=Path(__file__).resolve().parents[1]


def run(*args,timeout=60,**kwargs):
    result=subprocess.run(args,text=True,capture_output=True,timeout=timeout,**kwargs)
    if result.returncode:
        raise RuntimeError(f"{args!r}: {result.stdout}\n{result.stderr}")
    return result.stdout.strip()


def main():
    run("go","build","-o",str(ROOT/"bin/jag"),"./cmd/jag",cwd=ROOT)
    jag=str(ROOT/"bin/jag")
    project="jag-update-test-"+uuid.uuid4().hex[:8]
    image_override=ROOT/"deploy"/(".jag-update-"+project+".json")
    with tempfile.TemporaryDirectory(prefix="jag-update-") as directory:
        temp=Path(directory)
        run("ssh-keygen","-q","-t","ed25519","-N","","-f",str(temp/"key"))
        with socket.socket() as sock:
            sock.bind(("127.0.0.1",0))
            port=sock.getsockname()[1]
        env_file=temp/"deployment.env"
        env_file.write_text(f"CODEX_VERSION=0.165.0\nNODE_VERSION=22.22.0\nJAG_SSH_BIND=127.0.0.1\nJAG_SSH_PORT={port}\nAUTHORIZED_KEYS_FILE={temp}/key.pub\nGITLAB_TOKEN_FILE=/dev/null\nGITEA_TOKEN_FILE=/dev/null\n")
        image_override.write_text(json.dumps({"services":{"agent":{"image":"jag-agent:update-from"}}}))
        # The updater builds from the checked-out source, with this initial-image override.
        # Merge into a single temporary Compose file to keep the host command's interface simple.
        compose=ROOT/"deploy"/(".jag-update-"+project+"-compose.json")
        base=["docker","compose","-p",project,"--env-file",str(env_file),"-f",str(ROOT/"deploy/compose.yaml"),"-f",str(image_override)]
        config=json.loads(run(*base,"config","--format","json"))
        # Docker's seccomp path must remain relative to deploy/.
        config.pop("name",None)
        compose.write_text(json.dumps(config))
        deployed=["docker","compose","-p",project,"--env-file",str(env_file),"-f",str(compose)]
        command=[jag,"host","update","--project",project,"--compose",str(compose),"--env-file",str(env_file),"--version","0.166.0"]
        try:
            run(*deployed,"up","-d","--no-build","--wait","--wait-timeout","60",timeout=90)
            container=run(*deployed,"ps","-q","agent")
            fingerprint=run("docker","exec",container,"cat","/var/lib/jag/ssh/ssh_host_ed25519_key.pub")
            run("docker","exec","-u","agent",container,"sh","-c","printf preserved > /home/agent/persistent-fixture")
            check=run(*command[:-2],"--check")
            assert "update available" in check,check
            known=temp/"known_hosts"
            known.write_text(f"[127.0.0.1]:{port} {fingerprint}\n")
            ssh=subprocess.Popen(["ssh","-F","/dev/null","-N","-T","-o","BatchMode=yes","-o","IdentitiesOnly=yes","-o","StrictHostKeyChecking=yes","-o",f"UserKnownHostsFile={known}","-i",str(temp/"key"),"-p",str(port),"agent@127.0.0.1"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            try:
                deadline=time.monotonic()+10
                while time.monotonic()<deadline:
                    status=json.loads(run("docker","exec","-u","agent",container,"jag","_maintenance","status"))
                    if status["Busy"] and "SSH" in status["Reason"]:
                        break
                    time.sleep(0.1)
                assert status["Busy"] and "SSH" in status["Reason"],status
                assert "Deferred" in run(*command)
                assert run(*deployed,"ps","-q","agent")==container
            finally:
                ssh.terminate()
                ssh.wait(timeout=10)
            deadline=time.monotonic()+10
            while time.monotonic()<deadline:
                status=json.loads(run("docker","exec","-u","agent",container,"jag","_maintenance","status"))
                if not status["Busy"]:
                    break
                time.sleep(0.1)
            assert not status["Busy"],status
            print("PASS: open raw SSH connection defers automatic replacement",flush=True)
            # Detached terminals must prevent replacement, even when their agent is idle.
            run("docker","exec","-u","agent",container,"tmux","-L","jag","new-session","-d","-s","busy-test","sleep","300")
            result=run(*command,timeout=60)
            assert "Deferred" in result,result
            assert run(*deployed,"ps","-q","agent")==container
            assert "CODEX_VERSION=0.165.0" in env_file.read_text()
            run("docker","exec","-u","agent",container,"tmux","-L","jag","kill-server")
            print("PASS: detached terminal defers update and preserves the current container",flush=True)
            result=run(*command,timeout=600)
            assert "Updated Codex 0.165.0" in result,result
            updated=run(*deployed,"ps","-q","agent")
            assert updated!=container
            assert run("docker","exec","-u","agent",updated,"codex","--version")=="codex-cli 0.166.0"
            assert run("docker","exec",updated,"cat","/home/agent/persistent-fixture")=="preserved"
            assert run("docker","exec",updated,"cat","/var/lib/jag/ssh/ssh_host_ed25519_key.pub")==fingerprint
            assert "CODEX_VERSION=0.166.0" in env_file.read_text()
            status=json.loads(run("docker","exec","-u","agent",updated,"jag","_maintenance","status"))
            assert not status["Busy"],status
            lease="test-admission-gate"
            admission=json.loads(run("docker","exec","-u","agent",updated,"jag","_maintenance","begin",lease))
            assert not admission["Busy"],admission
            request=base64.urlsafe_b64encode(json.dumps({"op":"create","id":"w-gate-test"}).encode()).decode().rstrip("=")
            blocked=subprocess.run(["docker","exec","-u","agent",updated,"jag","_rpc",request],text=True,capture_output=True,timeout=30)
            assert blocked.returncode!=0 and "environment is updating" in blocked.stderr,blocked.stderr
            run("docker","exec","-u","agent",updated,"jag","_maintenance","end",lease)
            print("PASS: real candidate build/sandbox validation, update, persistent data/host identity and committed pin",flush=True)

            # Fail the candidate's health check while allowing the old version to recover.
            env_file.write_text(env_file.read_text().replace("CODEX_VERSION=0.166.0","CODEX_VERSION=0.165.0"))
            config["services"]["agent"]["healthcheck"]={"test":["CMD-SHELL","codex --version | grep -q '0.165.0'"],"interval":"1s","timeout":"5s","retries":1}
            compose.write_text(json.dumps(config))
            run(*deployed,"up","-d","--no-build","--force-recreate","--wait","--wait-timeout","60",timeout=90)
            failed=subprocess.run(command,text=True,capture_output=True,timeout=600)
            assert failed.returncode!=0 and "previous image restored" in failed.stderr,failed.stdout+failed.stderr
            restored=run(*deployed,"ps","-q","agent")
            assert run("docker","exec","-u","agent",restored,"codex","--version")=="codex-cli 0.165.0"
            assert "CODEX_VERSION=0.165.0" in env_file.read_text()
            assert run("docker","exec",restored,"cat","/home/agent/persistent-fixture")=="preserved"
            assert not json.loads(run("docker","exec","-u","agent",restored,"jag","_maintenance","status"))["Busy"]
            print("PASS: real failed health check restores previous image, retains pin/data and releases gate",flush=True)
        finally:
            subprocess.run([*deployed,"down","-v","--remove-orphans"],capture_output=True,timeout=60)
            images=subprocess.run(["docker","image","ls","--filter",f"reference=jag-update-{project}:*","--format","{{.Repository}}:{{.Tag}}"],text=True,capture_output=True,timeout=30)
            if images.returncode==0 and images.stdout.strip():
                subprocess.run(["docker","image","rm",*images.stdout.splitlines()],capture_output=True,timeout=30)
            image_override.unlink(missing_ok=True)
            compose.unlink(missing_ok=True)


if __name__=="__main__":
    main()
