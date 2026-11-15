#!/usr/bin/env python3
"""Optional native recording/minutes setup, installed on top of Checkpoint."""
import argparse
import json
import os
from pathlib import Path
import pwd
import secrets
import subprocess
import sys


def run(*args):
    subprocess.run(args, check=True)


def private(path, text, user, replace=False):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists() and not replace:
        return
    temporary = path.with_name(path.name + '.pending')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, 'w') as out:
            out.write(text)
            out.flush()
            os.fsync(out.fileno())
        account = pwd.getpwnam(user)
        os.chown(temporary, account.pw_uid, account.pw_gid)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def unit(kind, command, memory, cpu):
    user = 'checkpoint-' + kind
    return f'''[Unit]
Description=Checkpoint {kind}
After=checkpoint-core.service network-online.target
Wants=network-online.target

[Service]
User={user}
Group={user}
SupplementaryGroups=checkpoint-media
ExecStart={command}
Restart=on-failure
RestartSec=3
TimeoutStopSec=25
KillMode=control-group
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
CapabilityBoundingSet=
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
StateDirectory={user}
StateDirectoryMode=0700
ReadWritePaths=/var/lib/{user}
InaccessiblePaths=/var/lib/checkpoint
Environment=CHECKPOINT_MINUTES_CONFIG=/etc/checkpoint/media/{kind}.yaml
Environment=CHECKPOINT_MEDIA_TOKEN_FILE=/etc/checkpoint/media/{kind}.token
Environment=CHECKPOINT_WORKER_STORAGE=/var/lib/{user}
Environment=HF_HUB_OFFLINE=1
Environment=TRANSFORMERS_OFFLINE=1
Nice=10
CPUWeight=10
CPUQuota={cpu}%
MemoryMax={memory}
TasksMax=128

[Install]
WantedBy=multi-user.target
'''


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', type=Path, help='Explicit profile to apply; existing configuration is otherwise retained')
    parser.add_argument('--record-only', action='store_true', help='Do not install models or the processor')
    parser.add_argument('--prepare-only', action='store_true', help='Prepare services without restarting core')
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise RuntimeError('Run native setup locally as root')
    base = Path(__file__).resolve().parent.parent
    binary = Path('/opt/checkpoint/current/checkpoint')
    if not binary.is_file() or not Path('/run/systemd/system').is_dir():
        raise RuntimeError('Install the base Checkpoint system on a systemd VPS first')
    try:
        import urllib.request
        health = json.load(urllib.request.urlopen('http://127.0.0.1:18180/api/media-health', timeout=3))
    except Exception:
        health = {}
    if health.get('recordings', 0):
        raise RuntimeError('Stop active recordings first; setup does not restart the recorder implicitly')
    run('apt-get', 'update')
    packages = ['ffmpeg', 'python3-venv', 'cmake', 'make', 'g++', 'ca-certificates']
    run('apt-get', 'install', '-y', '--no-install-recommends', *packages)
    run('groupadd', '--system', '--force', 'checkpoint-media')
    run('usermod', '-a', '-G', 'checkpoint-media', 'checkpoint')
    for kind in ('recorder', 'processor', 'generator'):
        user = 'checkpoint-' + kind
        try:
            pwd.getpwnam(user)
        except KeyError:
            run('useradd', '--system', '--no-create-home', '--shell', '/usr/sbin/nologin', user)
        run('usermod', '-a', '-G', 'checkpoint-media', user)
    media = Path('/etc/checkpoint/media')
    media.mkdir(mode=0o755, exist_ok=True)
    lock = json.loads((base / 'deploy/minutes/models.lock.json').read_text())
    prefix = Path('/opt/checkpoint-minutes')
    if not args.record_only:
        run('python3', '-m', 'venv', str(prefix / 'venv'))
        run(str(prefix / 'venv/bin/pip'), 'install', '--require-hashes', '-r', str(base / 'deploy/minutes/requirements.lock'))
        run('python3', str(base / 'scripts/minutes-assets.py'), '--prefix', str(prefix))
    config = Path('/etc/checkpoint/minutes.yaml')
    if args.config:
        run(str(binary), 'minutes', 'config', 'validate', '--file', str(args.config))
        text = args.config.read_text()
    elif config.exists():
        text = config.read_text()
    else:
        asr = next(m for m in lock['models'] if 'whisper' in m['id'])
        llm = next(m for m in lock['models'] if 'Qwen' in m['id'])
        text = json.dumps({'enabled': True, 'transcription': {'revision': asr['revision']},
                           'generator': {'model': 'Qwen3-4B-Q4_K_M', 'revision': llm['revision']}})
    # Resolve through the same CLI schema; no second YAML interpretation.
    staged = media / 'proposed.yaml'
    private(staged, text, 'root', replace=True)
    effective = json.loads(subprocess.check_output([str(binary), 'minutes', 'config', 'show'],
        env={**os.environ, 'CHECKPOINT_MINUTES_CONFIG': str(staged)}, text=True))
    staged.unlink()
    if effective['storage'] != '/var/lib/checkpoint/recordings' or effective['socket'] != '/run/checkpoint-media/core.sock':
        raise RuntimeError('Native setup requires the default storage and socket paths; configure systemd units manually for custom paths')
    if not args.record_only and effective['generator']['adapter'] == 'local':
        if effective['generator']['endpoint'] != 'http://127.0.0.1:18089/v1/chat/completions':
            raise RuntimeError('The native local generator uses only 127.0.0.1:18089')
        supplied = effective['generator'].get('credentialFile')
        if supplied and supplied != str(media / 'processor-generator.token'):
            private(media / 'processor-generator.token', Path(supplied).read_text(), 'checkpoint-processor', replace=True)
        keyfile = media / 'processor-generator.token'
        if not keyfile.exists():
            private(keyfile, secrets.token_hex(32), 'checkpoint-processor')
        private(media / 'generator-api.token', keyfile.read_text(), 'checkpoint-generator', replace=True)
        effective['generator']['credentialFile'] = str(keyfile)
    text = json.dumps(effective, indent=2) + '\n'
    private(config, text, 'checkpoint', replace=True)
    # Worker configuration contains settings and credential paths, never API keys.
    for kind in ('recorder', 'processor'):
        private(media / (kind + '.yaml'), text, 'checkpoint-' + kind, replace=True)
        token = media / (kind + '.token')
        if not token.exists():
            private(token, secrets.token_hex(32), 'checkpoint-' + kind)
        private(media / ('core-' + kind + '.token'), token.read_text(), 'checkpoint', replace=True)
    units = Path('/etc/systemd/system')
    # Only the socket directory is shared. Archive/database/token files stay private.
    dropin = units / 'checkpoint-core.service.d/minutes.conf'
    dropin.parent.mkdir(parents=True, exist_ok=True)
    dropin.write_text('[Service]\nSupplementaryGroups=checkpoint-media\nRuntimeDirectory=checkpoint-media\nRuntimeDirectoryMode=0750\nReadWritePaths=/run/checkpoint-media\nExecStartPre=+/usr/bin/chgrp checkpoint-media /run/checkpoint-media\nEnvironment=CHECKPOINT_RECORDER_TOKEN_FILE=/etc/checkpoint/media/core-recorder.token\nEnvironment=CHECKPOINT_PROCESSOR_TOKEN_FILE=/etc/checkpoint/media/core-processor.token\n')
    # systemd reapplies RuntimeDirectory ownership before each Exec command,
    # including ExecStartPost. Set the directory group here as well as the socket;
    # keep the core UID/primary group and private archive permissions unchanged.
    with dropin.open('a') as output:
        output.write('ExecStartPost=+/bin/sh -c "chgrp checkpoint-media /run/checkpoint-media; for i in $(seq 1 50); do test ! -S /run/checkpoint-media/core.sock || { chgrp checkpoint-media /run/checkpoint-media/core.sock; exit 0; }; sleep 0.1; done; exit 1"\n')
    (units / 'checkpoint-recorder.service').write_text(unit('recorder', str(binary) + ' minutes worker recorder', '1G', 100))
    if not args.record_only:
        (units / 'checkpoint-processor.service').write_text(unit('processor', str(binary) + ' minutes worker processor', '3G', 150))
        command = f'{prefix}/llama/llama-server -m {prefix}/models/generator/Qwen3-4B-Q4_K_M.gguf --host 127.0.0.1 --port 18089 -t 2 -c 8192 --parallel 1 --jinja --reasoning-budget 0 --api-key-file /etc/checkpoint/media/generator-api.token'
        (units / 'checkpoint-generator.service').write_text(unit('generator', command, '5G', 150))
    run('systemctl', 'daemon-reload')
    if not args.prepare_only:
        # Explicit installer action; operators must finish recordings before rerun.
        run('systemctl', 'restart', 'checkpoint-core')
        run('systemctl', 'enable', '--now', 'checkpoint-recorder')
        if not args.record_only:
            run('systemctl', 'enable', '--now', 'checkpoint-generator', 'checkpoint-processor')
    print('Recording/minutes services prepared. Diagnostics: checkpoint minutes doctor')


if __name__ == '__main__':
    main()
