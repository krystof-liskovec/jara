#!/usr/bin/env python3
# Fake ssh: runs the "remote" command locally with the remote host's
# environment, and emulates -N -R remote_socket:local_socket.
#
# Hosts: "fakehost" uses $E2E_HUB; any other host H uses $E2E_HOST_H. With
# -F, an alias resolves through that file's HostName, and a connection made
# with a host's peer key (IdentityFile) runs the target's forced command
# `jara _peer NAME` when the target authorized that key, as sshd would.
# -G prints the resolved configuration.
import os, sys, socket, threading, subprocess
args = sys.argv[1:]
reverse = None; noexec = False; config = None; resolve = False; i = 0
takes = {"-o","-p","-l","-i","-F","-R","-L"}
while i < len(args):
    a = args[i]
    if a == "--": i += 1; break
    if not a.startswith("-"): break
    if a == "-R": reverse = args[i+1]
    if a == "-F": config = args[i+1]
    if a == "-N": noexec = True
    if a == "-G": resolve = True
    i += 2 if a in takes else 1
host = args[i]; command = " ".join(args[i+1:])
if resolve:
    print(f"hostname {host}\nport 22\nuser agent\nproxyjump none")
    sys.exit(0)
identity = None
if config:
    current = None
    for line in open(config):
        parts = line.split()
        if len(parts) < 2: continue
        key, value = parts[0].lower(), parts[1]
        if key == "host": current = value
        elif current == host and key == "hostname": target = value
        elif current == host and key == "identityfile": identity = value
    if identity: host = target
def home_of(name):
    return os.environ["E2E_HUB"] if name == "fakehost" else os.environ["E2E_HOST_" + name]
hub = home_of(host)
env = dict(os.environ)
env.update({"JARA_DATA": hub + "/data", "CODEX_HOME": hub + "/codex", "CLAUDE_CONFIG_DIR": hub + "/claude",
            "JARA_CONFIG": hub + "/config.json", "HOME": hub + "/home"})
env.pop("JARA_SSH_CONFIG", None)
if identity:
    # sshd: the key must be authorized there, and its forced command runs.
    key = " ".join(open(identity + ".pub").read().split()[:2])
    peer = None
    directory = hub + "/data/peers/authorized"
    for name in os.listdir(directory) if os.path.isdir(directory) else []:
        if open(os.path.join(directory, name)).read().strip() == key:
            peer = name[:-len(".pub")]
    if peer is None:
        print("Permission denied (publickey).", file=sys.stderr); sys.exit(255)
    env["SSH_ORIGINAL_COMMAND"] = command
    command = "jara _peer " + peer
def relay(remote, local):
    try: os.unlink(remote)
    except FileNotFoundError: pass
    server = socket.socket(socket.AF_UNIX); server.bind(remote); server.listen(8)
    def pipe(a, b):
        try:
            while True:
                d = a.recv(65536)
                if not d: break
                b.sendall(d)
        except OSError: pass
        for s in (a, b):
            try: s.shutdown(socket.SHUT_RDWR)
            except OSError: pass
    while True:
        conn, _ = server.accept()
        up = socket.socket(socket.AF_UNIX)
        try: up.connect(local)
        except OSError: conn.close(); continue
        threading.Thread(target=pipe, args=(conn, up), daemon=True).start()
        threading.Thread(target=pipe, args=(up, conn), daemon=True).start()
if reverse:
    remote, local = reverse.split(":", 1)
    threading.Thread(target=relay, args=(remote, local), daemon=True).start()
    if noexec:
        threading.Event().wait()
sys.exit(subprocess.call(["sh", "-c", command], env=env))
