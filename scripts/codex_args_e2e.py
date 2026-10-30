#!/usr/bin/env python3
"""Real startup environment + SSH/client argument forwarding; no model calls."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time
import uuid

from real_codex_e2e import ROOT, Terminal, run


def main():
    run('go', 'build', '-o', str(ROOT / 'bin/jara'), './cmd/jara', cwd=ROOT)
    with tempfile.TemporaryDirectory(prefix='jara-args-') as directory:
        temp = Path(directory)
        temp.chmod(0o755)
        key = temp / 'key'
        run('ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(key))
        name = 'jara-args-' + uuid.uuid4().hex[:8]
        terminal = None
        defaults = ['--model', 'environment-model', '-c', 'literal="space $HOME `id` $(touch /tmp/jara-argument-eval)"']
        try:
            run('docker', 'run', '-d', '--name', name, '-p', '127.0.0.1::22',
                '-e', 'JARA_CODEX_ARGS=' + json.dumps(defaults),
                '-v', f'{key}.pub:/run/jara/authorized_keys:ro',
                os.environ.get('JARA_TEST_IMAGE', 'jara-smoke-agent'))
            deadline = time.monotonic() + 15
            while True:
                try:
                    public = run('docker', 'exec', name, 'cat', '/var/lib/jara/ssh/ssh_host_ed25519_key.pub')
                    assert json.loads(run('docker', 'exec', name, 'cat', '/run/jara/codex-args.json')) == defaults
                    break
                except (RuntimeError, AssertionError):
                    if time.monotonic() > deadline: raise
                    time.sleep(.1)
            port = run('docker', 'port', name, '22/tcp').rsplit(':', 1)[1]
            (temp / 'known_hosts').write_text(f'[127.0.0.1]:{port} {public}\n')
            (temp / 'ssh_config').write_text(f'Host fixture\n HostName 127.0.0.1\n Port {port}\n User agent\n IdentityFile {key}\n IdentitiesOnly yes\n UserKnownHostsFile {temp}/known_hosts\n')
            mock = '''#!/bin/sh
if [ "$1" = login ]; then exit 1; fi
if [ "$1" = sandbox ]; then exit 0; fi
exec /usr/local/bin/node -e 'console.log("JARA_BG:"+process.env.JARA_TMUX_BACKGROUND);console.log("JARA_ARGS:"+JSON.stringify(process.argv.slice(1)));setInterval(()=>{},1000)' -- "$@"
'''
            run('docker', 'exec', '-i', name, 'sh', '-c', 'cat > /usr/local/bin/codex', input=mock)
            env = dict(os.environ, JARA_CONFIG=str(temp / 'config.json'),
                       JARA_SSH_CONFIG=str(temp / 'ssh_config'), TERM='xterm-256color')
            jara = str(ROOT / 'bin/jara')
            run(jara, 'remote', 'add', 'fixture', '--host', 'fixture', env=env)
            workspace = temp / 'workspace'
            workspace.mkdir()

            def check(argv, expected):
                nonlocal terminal
                terminal = Terminal(argv, workspace, env)
                marker = 'JARA_ARGS:' + json.dumps(expected, separators=(',', ':'))
                background = 'JARA_BG:#123456'
                deadline = time.monotonic() + 15
                while time.monotonic() < deadline:
                    terminal.drain()
                    result = subprocess.run(['docker', 'exec', '-u', 'agent', name, 'tmux', '-L', 'jara', 'capture-pane', '-p', '-J'], text=True, capture_output=True)
                    if marker in result.stdout and background in result.stdout: break
                    if terminal.process.poll() is not None: raise AssertionError(terminal.output.decode(errors='replace'))
                    time.sleep(.1)
                else: raise AssertionError(marker + '\n' + result.stdout)
                session = run('docker', 'exec', '-u', 'agent', name, 'tmux', '-L', 'jara', 'list-sessions', '-F', '#{session_name}')
                style = run('docker', 'exec', '-u', 'agent', name, 'tmux', '-L', 'jara', 'show-options', '-w', '-v', '-t', session + ':0', 'window-style')
                assert style == 'bg=#123456', f'terminal color was not detected: {style}'
                terminal.close()
                terminal = None

            extra = ['--search', '-c', 'client="two words and $(untouched)"']
            check([jara, '--copy', '--codex-arg=--search', '--', *extra[1:]], defaults + extra)
            # Arguments on reattach are acknowledged but do not restart Codex.
            check([jara, '--', '--model', 'ignored-on-attach'], defaults + extra)
            assert len(run('docker', 'exec', '-u', 'agent', name, 'tmux', '-L', 'jara', 'list-sessions').splitlines()) == 1
            run('docker', 'exec', '-u', 'agent', name, 'tmux', '-L', 'jara', 'kill-server')
            check([jara, 'resume', '--', '--all'], ['resume', *defaults, '--all'])
            binding = json.loads((temp / 'config.json').read_text())['bindings'][str(workspace)]
            run('docker', 'exec', '-u', 'agent', name, 'tmux', '-L', 'jara', 'kill-server')
            command = shlex.join(['jara', 'connect', '--resume', '--codex-arg=--all', binding['workspace']])
            check(['ssh', '-tt', '-F', str(temp / 'ssh_config'), 'fixture', command], ['resume', *defaults, '--all'])
            run('docker', 'exec', name, 'test', '!', '-e', '/tmp/jara-argument-eval')
            run('docker', 'exec', '-u', 'agent', name, 'tmux', '-L', 'jara', 'kill-server')
            run('docker', 'exec', '-i', name, 'sh', '-c', 'cat > /run/jara/codex-args.json', input='[]')
            check([jara], [])
            print('PASS: environment array, client flags/passthrough, literal quoting, resume, plain SSH, reattach and empty defaults', flush=True)
        finally:
            if terminal: terminal.close(abrupt=True)
            subprocess.run(['docker', 'rm', '-fv', name], capture_output=True)


if __name__ == '__main__':
    main()
