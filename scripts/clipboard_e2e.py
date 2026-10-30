#!/usr/bin/env python3
"""Local clipboard -> SSH upload -> real Codex image attachment. No model calls.

Uses a fake local clipboard provider and a dummy API key. Codex's API endpoint
is an unreachable container-local address; no personal credentials are imported.
"""
import fcntl
import json
import os
from pathlib import Path
import re
import shlex
import signal
import struct
import subprocess
import tempfile
import termios
import time
import uuid
import zlib

from real_codex_e2e import ROOT, Terminal, run


def png_fixture():
    def chunk(kind, data):
        return struct.pack('!I', len(data)) + kind + data + struct.pack('!I', zlib.crc32(kind + data))
    return (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('!2I5B', 2, 2, 8, 2, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress(b'\0\xff\0\0\0\xff\0' * 2)) + chunk(b'IEND', b''))


def main():
    run('go', 'build', '-o', str(ROOT / 'bin/jara'), './cmd/jara', cwd=ROOT)
    with tempfile.TemporaryDirectory(prefix='jara-clipboard-') as directory:
        temp = Path(directory)
        temp.chmod(0o755)
        key = temp / 'key'
        run('ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(key))
        container = 'jara-clipboard-' + uuid.uuid4().hex[:8]
        terminal = None
        try:
            run('docker', 'run', '-d', '--name', container, '-p', '127.0.0.1::22',
                '--security-opt', f'seccomp={ROOT / "deploy/seccomp/codex.json"}',
                '-v', f'{key}.pub:/run/jara/authorized_keys:ro',
                os.environ.get('JARA_TEST_IMAGE', 'jara-smoke-agent'))
            run('docker', 'cp', str(ROOT / 'bin/jara'), f'{container}:/opt/jara/bin/jara')
            port = run('docker', 'port', container, '22/tcp').rsplit(':', 1)[1]
            public = run('docker', 'exec', container, 'cat', '/var/lib/jara/ssh/ssh_host_ed25519_key.pub')
            (temp / 'known_hosts').write_text(f'[127.0.0.1]:{port} {public}\n')
            (temp / 'ssh_config').write_text(f'Host fixture\n HostName 127.0.0.1\n Port {port}\n User agent\n IdentityFile {key}\n IdentitiesOnly yes\n UserKnownHostsFile {temp}/known_hosts\n')
            run('docker', 'exec', '-u', 'agent', container, 'mkdir', '-p', '/home/agent/.codex')
            run('docker', 'exec', '-i', '-u', 'agent', container, 'sh', '-c',
                'cat > /home/agent/.codex/auth.json', input=json.dumps({'OPENAI_API_KEY': 'sk-jara-offline-fixture'}))
            run('docker', 'exec', '-i', '-u', 'agent', container, 'sh', '-c',
                'cat > /home/agent/.codex/config.toml',
                input='model = "gpt-6.1-sol"\nmodel_provider = "offline-fixture"\n[model_providers.offline-fixture]\nname = "Offline fixture"\nwire_api = "responses"\nrequires_openai_auth = true\nbase_url = "http://127.0.0.1:1/v1"\n')
            run('docker', 'exec', '-u', 'agent', container, 'codex', 'sandbox', '--', 'true')
            image = temp / 'fixture.png'
            image.write_bytes(png_fixture())
            provider = temp / 'wl-paste'
            provider.write_text('#!/bin/sh\nexec cat ' + shlex.quote(str(image)) + '\n')
            provider.chmod(0o755)
            env = dict(os.environ, JARA_CONFIG=str(temp / 'config.json'),
                       JARA_SSH_CONFIG=str(temp / 'ssh_config'), TERM='xterm-256color',
                       WAYLAND_DISPLAY='fixture', PATH=str(temp) + ':' + os.environ['PATH'])
            jara = str(ROOT / 'bin/jara')
            run(jara, 'remote', 'add', 'fixture', '--host', 'fixture', env=env)
            workspace = temp / 'workspace'
            workspace.mkdir()
            terminal = Terminal([jara, '--copy', '--codex-arg=--dangerously-bypass-approvals-and-sandbox'], workspace, env)

            def screen():
                terminal.drain()
                result = subprocess.run(['docker', 'exec', '-u', 'agent', container, 'tmux', '-L', 'jara',
                                         'capture-pane', '-p'], capture_output=True, text=True)
                return result.stdout

            def composer_line():
                result = subprocess.run(['docker', 'exec', '-u', 'agent', container, 'tmux', '-L', 'jara',
                                         'capture-pane', '-e', '-p'], capture_output=True, text=True)
                return next(line for line in result.stdout.splitlines() if 'Ask Codex to do anything' in line)

            def until(check, message):
                deadline = time.monotonic() + 25
                while time.monotonic() < deadline:
                    if check(): return
                    if terminal.process.poll() is not None: break
                    time.sleep(.1)
                raise AssertionError(message + '\n' + screen() + '\n' + terminal.output.decode(errors='replace'))

            until(lambda: 'permissions: YOLO mode' in screen() and 'GPT-6.1-Sol' in screen(),
                  'Codex composer did not finish startup')
            time.sleep(.5)
            session = run('docker', 'exec', '-u', 'agent', container, 'tmux', '-L', 'jara',
                          'list-sessions', '-F', '#{session_name}')
            style = run('docker', 'exec', '-u', 'agent', container, 'tmux', '-L', 'jara',
                        'show-options', '-w', '-v', '-t', session + ':0', 'window-style')
            assert style == 'bg=#123456', f'terminal color was not detected: {style}'
            color_env = run('docker', 'exec', '-u', 'agent', container, 'tmux', '-L', 'jara',
                            'show-environment', '-t', session, 'JARA_TMUX_BACKGROUND')
            assert color_env == 'JARA_TMUX_BACKGROUND=#123456', color_env
            line = composer_line()
            assert re.search(r'\x1b\[[0-9;]*48;(?:2|5);', line), f'Codex composer background is unset: {line!r}'
            terminal.send(b'\x1b[200~draft before image \x1b[201~')
            terminal.send(b'\x16')
            until(lambda: '[Image #1]' in screen(), 'Ctrl+V did not attach an image in real Codex')
            assert 'draft before image' in screen(), screen()
            paths = run('docker', 'exec', container, 'find', '/home/agent/.local/share/jara/attachments', '-type', 'f').splitlines()
            assert len(paths) == 1, paths
            actual = subprocess.check_output(['docker', 'exec', container, 'cat', paths[0]])
            assert actual == image.read_bytes(), 'transferred image changed'
            print('PASS: local Ctrl+V transfers exact PNG and real Codex displays [Image #1] beside the unsubmitted draft', flush=True)
            provider.write_text('#!/bin/sh\nexit 1\n')
            terminal.send(b'\x16')
            until(lambda: b'jara: cannot read a clipboard image' in terminal.output or
                  (screen() and b'jara: cannot read a clipboard image' in terminal.output),
                  'clipboard failure was not reported locally')
            assert 'draft before image' in screen() and '[Image #1]' in screen(), screen()
            assert b'Failed to paste image' not in terminal.output, 'Ctrl+V leaked to remote clipboard'
            provider.write_text('#!/bin/sh\nexec cat ' + shlex.quote(str(image)) + '\n')
            terminal.send(b'\x1b[118;5u')
            until(lambda: '[Image #2]' in screen(), 'extended Ctrl+V did not attach the second image')
            print('PASS: missing clipboard image preserves draft; extended Ctrl+V attaches a second image', flush=True)
            fcntl.ioctl(terminal.master, termios.TIOCSWINSZ, struct.pack('HHHH', 28, 70, 0, 0))
            os.kill(terminal.process.pid, signal.SIGWINCH)
            until(lambda: run('docker', 'exec', '-u', 'agent', container, 'tmux', '-L', 'jara',
                              'list-clients', '-F', '#{client_width}x#{client_height}') == '70x28',
                  'resize did not reach remote tmux')
            terminal.close()
            terminal = None
            print('PASS: resize and Ctrl-b d work through the clipboard terminal proxy', flush=True)
        finally:
            if terminal: terminal.close(abrupt=True)
            subprocess.run(['docker', 'rm', '-fv', container], capture_output=True)


if __name__ == '__main__':
    main()
