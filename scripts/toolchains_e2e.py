#!/usr/bin/env python3
"""Real concurrent Node/Go toolchains and real Codex startup; no model calls.

Build jag-agent:toolchains-validation from the current tree before running.
"""
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import tempfile
import time
import uuid

from real_codex_e2e import ROOT, Terminal, run


def main():
    run("go","build","-o",str(ROOT/"bin/jag"),"./cmd/jag",cwd=ROOT)
    project="jag-toolchains-"+uuid.uuid4().hex[:8]
    jag=str(ROOT/"bin/jag")
    with tempfile.TemporaryDirectory(prefix="jag-toolchains-") as directory:
        temp=Path(directory)
        run("ssh-keygen","-q","-t","ed25519","-N","","-f",str(temp/"key"))
        with socket.socket() as sock:
            sock.bind(("127.0.0.1",0))
            port=sock.getsockname()[1]
        override=temp/"compose.json"
        override.write_text(json.dumps({"services":{"agent":{"image":os.environ.get("JAG_TEST_IMAGE","jag-agent:toolchains-validation")}}}))
        env=dict(os.environ,CODEX_VERSION="0.166.0",JAG_SSH_BIND="127.0.0.1",JAG_SSH_PORT=str(port),AUTHORIZED_KEYS_FILE=str(temp/"key.pub"),GITLAB_TOKEN_FILE="/dev/null",GITEA_TOKEN_FILE="/dev/null")
        compose=["docker","compose","-p",project,"-f",str(ROOT/"deploy/compose.yaml"),"-f",str(override)]
        terminals=[]
        try:
            run(*compose,"up","-d","--no-build",env=env)
            container=run(*compose,"ps","-q","agent",env=env)
            deadline=time.monotonic()+15
            while True:
                try:
                    public=run("docker","exec",container,"cat","/var/lib/jag/ssh/ssh_host_ed25519_key.pub")
                    break
                except RuntimeError:
                    if time.monotonic()>deadline:
                        raise
                    time.sleep(0.1)
            (temp/"known_hosts").write_text(f"[127.0.0.1]:{port} {public}\n")
            ssh_config=temp/"ssh_config"
            ssh_config.write_text(f"Host fixture\n HostName 127.0.0.1\n Port {port}\n User agent\n IdentityFile {temp}/key\n IdentitiesOnly yes\n UserKnownHostsFile {temp}/known_hosts\n")
            client_env=dict(env,HOME=str(temp),JAG_CONFIG=str(temp/"config.json"),JAG_SSH_CONFIG=str(ssh_config),TERM="xterm-256color")
            for key in ("OPENAI_API_KEY","CODEX_API_KEY","CODEX_HOME"):
                client_env.pop(key,None)
            print(run(jag,"remote","add","fixture","--host","fixture",env=client_env),flush=True)
            a,b=temp/"project-a",temp/"project-b"
            a.mkdir();b.mkdir()
            (a/"mise.toml").write_text('[tools]\nnode = "20.20.0"\ngo = "1.26.0"\n')
            (a/"go.mod").write_text('module jag.test/project-a\n\ngo 1.26.0\n')
            (b/".nvmrc").write_text('22.22.0\n')
            (b/"go.mod").write_text('module jag.test/project-b\n\ngo 1.27.3\n')
            for directory in (a,b):
                (directory/"main.go").write_text('package main\nimport "fmt"\nfunc main(){fmt.Println("toolchains-ok")}\n')

            def remote_path(directory):
                binding=json.loads((temp/"config.json").read_text())["bindings"][str(directory)]
                return "/home/agent/.local/share/jag/workspaces/"+binding["workspace"],binding["workspace"]

            def session(workspace):
                output=run("docker","exec","-u","agent",container,"tmux","-L","jag","list-sessions","-F","#{session_name}|#{@jag_workspace}|#{session_attached}")
                return next((line.split("|")[0] for line in output.splitlines() if line.split("|")[1]==workspace),None)

            def start(directory):
                terminal=Terminal([jag,"--copy"],directory,client_env)
                terminals.append(terminal)
                deadline=time.monotonic()+300
                while time.monotonic()<deadline:
                    for current in terminals:
                        current.drain()
                    if terminal.process.poll() is not None:
                        raise AssertionError(terminal.output.decode(errors="replace"))
                    try:
                        path,workspace=remote_path(directory)
                        name=session(workspace)
                        if name:
                            screen=run("docker","exec","-u","agent",container,"tmux","-L","jag","capture-pane","-t",name,"-p")
                            if "Choose how you want to use Codex" in screen:
                                return path,workspace,name
                    except (FileNotFoundError,KeyError,RuntimeError):
                        pass
                    time.sleep(0.2)
                raise AssertionError("Tool preparation/Codex startup timed out:\n"+terminal.output.decode(errors="replace"))

            pa,wa,sa=start(a)
            print("PASS: project A installed Node 20.20.0 + Go 1.26.0 and launched real Codex",flush=True)
            pb,wb,sb=start(b)
            print("PASS: project B selected .nvmrc/go.mod versions alongside project A",flush=True)

            def ssh(command):
                return run("ssh","-F",str(ssh_config),"fixture",command,timeout=120)

            for path,node,go in ((pa,"v20.20.0","go1.26.0"),(pb,"v22.22.0","go1.27.3")):
                selected=ssh("cd "+shlex.quote(path)+" && node --version && go version && go run .")
                assert node in selected and go in selected and "toolchains-ok" in selected,selected
                login=ssh("cd "+shlex.quote(path)+" && bash -lc 'node --version; go version'")
                assert node in login and go in login,login
                fixed=ssh("cd "+shlex.quote(path)+" && mise exec -- /usr/local/bin/codex --version")
                assert fixed=="codex-cli 0.166.0",fixed
                # Codex masks a nonexistent .git with an empty directory. For a copied
                # non-Git folder, explicitly disable Go VCS stamping; don't change defaults.
                sandbox=ssh("cd "+shlex.quote(path)+" && mise exec -- /usr/local/bin/codex sandbox -c 'sandbox_mode=\"workspace-write\"' -- sh -c 'go build -buildvcs=false -o sandbox-built . && ./sandbox-built'")
                assert "toolchains-ok" in sandbox,sandbox
                ssh("cd "+shlex.quote(path)+" && git init -q && git add go.mod main.go && git -c user.name=Jag -c user.email=jag@example.invalid commit -qm fixture")
                sandbox=ssh("cd "+shlex.quote(path)+" && mise exec -- /usr/local/bin/codex sandbox -c 'sandbox_mode=\"workspace-write\"' -- sh -c 'go build -o sandbox-built . && ./sandbox-built'")
                assert "toolchains-ok" in sandbox,sandbox
                print("PASS: SSH, login shell, compiler and fixed Codex launcher:",node,go,flush=True)

            # The npm launcher execs the native binary; inspect only nonsecret runtime fields.
            inspect="""const fs=require('fs');let out=[];for(const id of fs.readdirSync('/proc').filter(x=>Number.isInteger(Number(x)))){try{const cwd=fs.readlinkSync('/proc/'+id+'/cwd');if(!process.argv.slice(1).includes(cwd))continue;const env=Object.fromEntries(fs.readFileSync('/proc/'+id+'/environ','utf8').split(String.fromCharCode(0)).map(x=>[x.slice(0,x.indexOf('=')),x.slice(x.indexOf('=')+1)]));out.push({cwd,exe:fs.readlinkSync('/proc/'+id+'/exe'),path:env.PATH,goroot:env.GOROOT});}catch{}}console.log(JSON.stringify(out));"""
            parents=json.loads(run("docker","exec","-u","agent",container,"/usr/local/bin/node","-e",inspect,pa,pb))
            for path,node,go in ((pa,"20.20.0","1.26.0"),(pb,"22.22.0","1.27.3")):
                process=next((p for p in parents if p["cwd"]==path and p["exe"].endswith("/bin/codex")),None)
                assert process is not None,parents
                assert "/@openai/codex/" in process["exe"] and "/mise/installs/" not in process["exe"],process
                assert "/node/"+node+"/" in process["path"] and "/go/"+go+"/" in process["path"],process
                assert process["goroot"].endswith("/go/"+go),process
            assert "exec /usr/local/bin/node" in run("docker","exec",container,"cat","/opt/jag/codex-launcher")
            print("PASS: two simultaneous real Codex processes have independent project PATHs and fixed launcher runtime",flush=True)
            assert len(run("docker","exec","-u","agent",container,"tmux","-L","jag","list-sessions").splitlines())==2
            for directory,node,go in ((a,"20.20.0","1.26.0"),(b,"22.22.0","1.27.3")):
                current=run(jag,"tools",cwd=directory,env=client_env)
                assert node in current and go in current,current

            if subprocess.run(["docker","exec",container,"sh","-c","command -v nvim"],capture_output=True).returncode==0:
                lua='lua vim.fn.writefile({vim.fn.trim(vim.fn.system("node --version")),vim.fn.trim(vim.fn.system("go version"))}, "editor-toolchain.txt")'
                for path,node,go in ((pa,"v20.20.0","go1.26.0"),(pb,"v22.22.0","go1.27.3")):
                    ssh(shlex.join(["jag","_tools","exec",path,"nvim","--headless","-u","NONE","-c",lua,"-c","qa!"]))
                    selected=ssh("cat "+shlex.quote(path+"/editor-toolchain.txt"))
                    assert node in selected and go in selected,selected
                print("PASS: actual Neovim processes invoke each project's Node/Go tools through Jag's editor helper",flush=True)

            # A bad configuration must fail visibly before a new tmux session is created.
            bad=temp/"bad-project";bad.mkdir();(bad/"mise.toml").write_text('[tools\nnode = "20.20.0"\n')
            failed=subprocess.run([jag,"--copy"],cwd=bad,env=client_env,text=True,capture_output=True,timeout=30)
            assert failed.returncode and "project tool installation failed" in failed.stdout+failed.stderr,failed.stdout+failed.stderr
            assert len(run("docker","exec","-u","agent",container,"tmux","-L","jag","list-sessions").splitlines())==2
            print("PASS: invalid project configuration reports setup failure without affecting running sessions",flush=True)
            for terminal in terminals:
                terminal.close()
            terminals=[]

            run(*compose,"up","-d","--no-build","--force-recreate",env=env)
            container=run(*compose,"ps","-q","agent",env=env)
            for path,node,go in ((pa,"v20.20.0","go1.26.0"),(pb,"v22.22.0","go1.27.3")):
                versions=run("docker","exec","--env","MISE_OFFLINE=1","-u","agent","-w",path,container,"mise","exec","--","sh","-c","node --version; go version")
                assert node in versions and go in versions,versions
            print("PASS: both toolchain versions survive container recreation and work offline",flush=True)
        finally:
            for terminal in terminals:
                terminal.close(abrupt=True)
            run(*compose,"down","-v","--remove-orphans",env=env)


if __name__=="__main__":
    main()
