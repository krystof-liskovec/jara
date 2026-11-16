#!/usr/bin/env python3
# Fake ssh: runs the "remote" command locally with the remote host's
# environment, and emulates -N -R remote_socket:local_socket.
import os, sys, socket, threading, subprocess
args = sys.argv[1:]
reverse = None; noexec = False; i = 0
takes = {"-o","-p","-l","-i","-F","-R","-L"}
while i < len(args):
    a = args[i]
    if a == "--": i += 1; break
    if not a.startswith("-"): break
    if a == "-R": reverse = args[i+1]
    if a == "-N": noexec = True
    i += 2 if a in takes else 1
host = args[i]; command = " ".join(args[i+1:])
env = dict(os.environ)
hub = os.environ["E2E_HUB"]
env.update({"JARA_DATA": hub + "/data", "CODEX_HOME": hub + "/codex", "CLAUDE_CONFIG_DIR": hub + "/claude",
            "JARA_CONFIG": hub + "/config.json", "HOME": hub + "/home"})
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
