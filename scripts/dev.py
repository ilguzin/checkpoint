#!/usr/bin/env python3
"""M0A only: own Compose project, secrets and individual Tailscale routes."""
import argparse
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import secrets
import socket
import string
import subprocess
import sys
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
STATE = ROOT / '.dev'
ROUTES = [(443, 18180), (9443, 18181), (9444, 18182)]
COMPOSE = ['docker', 'compose', '--env-file', str(STATE / 'compose.env'), '-f', str(ROOT / 'deploy/dev/compose.yaml')]

def run(args, capture=False):
    return subprocess.run(args, cwd=ROOT, text=True, check=True, stdout=subprocess.PIPE if capture else None, timeout=1200).stdout

def read(path):
    return json.loads(path.read_text())

def write(path, data):
    tmp = path.with_suffix(path.suffix + '.tmp')
    with open(tmp, 'w', opener=lambda p, flags: os.open(p, flags, 0o600)) as f:
        f.write(data if isinstance(data, str) else json.dumps(data, indent=2) + '\n')
        f.flush()
        os.fsync(f.fileno())
    tmp.replace(path)

def ts_status():
    return json.loads(run(['tailscale', 'status', '--json'], True))

def serve_status():
    return json.loads(run(['tailscale', 'serve', 'status', '--json'], True) or '{}')

def check_node():
    status = ts_status()
    if status.get('BackendState') != 'Running':
        raise RuntimeError('Tailscale must already be connected. This script does not register the node.')
    node = status['Self']
    ip = next(ip for ip in node['TailscaleIPs'] if ipaddress.ip_address(ip).version == 4)
    dns = node['DNSName'].rstrip('.')
    if dns not in (status.get('CertDomains') or []):
        raise RuntimeError('HTTPS is not enabled for this node in the tailnet. Enable it in the Tailscale console first.')
    return ip, dns

def prepare_recording():
    for name in ('media', 'media-run', 'recordings', 'recorder-data'):
        (STATE / name).mkdir(mode=0o700, exist_ok=True)
    config = STATE / 'media/minutes.yaml'
    if not config.exists():
        write(config, (ROOT / 'deploy/dev/minutes.yaml').read_text())
    for kind in ('recorder', 'processor'):
        path = STATE / 'media' / f'{kind}.token'
        if not path.exists():
            write(path, secrets.token_urlsafe(48) + '\n')

def prepare():
    run(['docker', 'compose', 'version'])
    ip, dns = check_node()
    prepare_recording()
    # Existing stands also need directories added by newer application versions.
    (STATE / 'data').mkdir(mode=0o700, exist_ok=True)
    (STATE / 'observer-data').mkdir(mode=0o700, exist_ok=True)
    if (STATE / 'access.json').exists():
        data = read(STATE / 'access.json')
        if data['ip'] != ip or data['dns'] != dns:
            raise RuntimeError('Node address changed. Stop the old stand and review .dev before reconfiguring.')
        print('Existing configuration and secrets preserved.')
        return
    if run(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=checkpoint-dev'], True).strip():
        raise RuntimeError('Existing checkpoint-dev containers without local state; refusing to adopt them.')
    data = {'ip': ip, 'dns': dns, 'key': 'CP' + secrets.token_hex(8), 'secret': secrets.token_hex(32),
            'meeting': secrets.token_urlsafe(32), 'host': secrets.token_urlsafe(32), 'viewer': secrets.token_urlsafe(32)}
    values = {'CP_UID': str(os.getuid()), 'CP_GID': str(os.getgid()), 'TAILNET_IP': ip,
              'LIVEKIT_API_KEY': data['key'], 'LIVEKIT_API_SECRET': data['secret'],
              'CHECKPOINT_PUBLIC_URL': f'https://{dns}', 'CHECKPOINT_SIGNAL_URL': f'wss://{dns}:9443',
              'CHECKPOINT_OBSERVER_URL': f'https://{dns}:9444',
              'CHECKPOINT_MEETING_INVITE': data['meeting'], 'CHECKPOINT_HOST_INVITE': data['host'], 'CHECKPOINT_VIEWER_INVITE': data['viewer']}
    write(STATE / 'compose.env', '\n'.join(f'{k}={values[k]}' for k in ('CP_UID','CP_GID','TAILNET_IP')) + '\n')
    write(STATE / 'runtime.env', '\n'.join(f'{k}={v}' for k, v in values.items() if k.startswith(('CHECKPOINT_', 'LIVEKIT_'))) + '\n')
    write(STATE / 'observer.env', f"LIVEKIT_API_KEY={data['key']}\nLIVEKIT_API_SECRET={data['secret']}\n")
    template = (ROOT / 'deploy/dev/livekit.yaml.template').read_text()
    write(STATE / 'livekit.yaml', string.Template(template).substitute(values))
    write(STATE / 'serve-before.json', serve_status())
    (STATE / 'data').mkdir(mode=0o700, exist_ok=True)
    (STATE / 'observer-data').mkdir(mode=0o700, exist_ok=True)
    write(STATE / 'access.json', data)
    print('Prepared private configuration in .dev (not tracked by Git).')

def port_available(ip, port, udp=False):
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM if udp else socket.SOCK_STREAM) as s:
            # Match server bind semantics: TIME_WAIT after down is not a foreign
            # listener. Never enable reuse for UDP, where it could hide a conflict.
            if not udp:
                s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind((ip, port))
            if not udp:
                s.listen(1)
    except OSError as error:
        raise RuntimeError(f'Port {"UDP" if udp else "TCP"} {ip}:{port} unavailable: {error}') from error

def route_value(config, dns, port):
    return config.get('Web', {}).get(f'{dns}:{port}')

def owned_route(config, dns, port, backend):
    return (route_value(config, dns, port) == {'Handlers': {'/': {'Proxy': f'http://127.0.0.1:{backend}'}}}
            and config.get('TCP', {}).get(str(port)) == {'HTTPS': True}
            and not config.get('AllowFunnel', {}).get(f'{dns}:{port}'))

def routes_add(data):
    config = serve_status()
    journal_path = STATE / 'routes.json'
    journal = read(journal_path) if journal_path.exists() else []
    for port, backend in ROUTES:
        existing = config.get('TCP', {}).get(str(port)) or route_value(config, data['dns'], port)
        if existing and not (port in journal and owned_route(config, data['dns'], port, backend)):
            raise RuntimeError(f'Serve port {port} has a foreign or changed configuration; refusing to overwrite it.')
    for port, backend in ROUTES:
        if owned_route(config, data['dns'], port, backend):
            continue
        # Journal intent before the mutation, so an interruption remains recoverable.
        if port not in journal:
            journal.append(port)
            write(journal_path, journal)
        run(['tailscale', 'serve', '--bg', '--yes', f'--https={port}', f'http://127.0.0.1:{backend}'])
        config = serve_status()
        if not owned_route(config, data['dns'], port, backend):
            raise RuntimeError(f'Unexpected Serve result on {port}; inspect configuration before continuing.')

def routes_remove(data):
    path = STATE / 'routes.json'
    if not path.exists():
        return
    journal = read(path)
    for port, backend in reversed(ROUTES):
        if port not in journal:
            continue
        config = serve_status()
        if owned_route(config, data['dns'], port, backend):
            run(['tailscale', 'serve', f'--https={port}', 'off'])
        elif config.get('TCP', {}).get(str(port)) or route_value(config, data['dns'], port):
            raise RuntimeError(f'Serve {port} changed since setup; left untouched. Inspect it manually.')
        journal.remove(port)
        write(path, journal)

def up():
    prepare()
    data = read(STATE / 'access.json')
    running = run(COMPOSE + ['ps', '-q', '--status', 'running'], True).strip()
    if not running:
        for port in (17880, 18180, 18181, 18182):
            port_available('127.0.0.1', port)
        port_available(data['ip'], 7881)
        port_available(data['ip'], 7882, udp=True)
    run(COMPOSE + ['up', '-d', '--build'])
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        try:
            for port in (18180, 18181, 18182):
                with urllib.request.urlopen(f'http://127.0.0.1:{port}/healthz', timeout=2) as r:
                    assert r.status == 200
            break
        except (OSError, AssertionError):
            time.sleep(1)
    else:
        raise RuntimeError('Readiness failed. Inspect: scripts/dev.sh logs')
    routes_add(data)
    print(f"App: https://{data['dns']}\nObserver: https://{data['dns']}:9444\nUse scripts/dev.sh links to show private invitation links.")
    print('Verify client ACL/grants for TCP 443, 9443, 9444, 7881 and UDP 7882; this script does not change tailnet policy.')

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['prepare','up','down','status','links','logs'])
    args = parser.parse_args()
    os.umask(0o077)
    STATE.mkdir(mode=0o700, exist_ok=True)
    with open(STATE / 'lock', 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if args.command == 'prepare': prepare()
        elif args.command == 'up': up()
        elif args.command == 'down':
            data = read(STATE / 'access.json')
            routes_remove(data)
            run(COMPOSE + ['down', '--timeout', '10'])
            print('Stand stopped. Secrets and test sessions preserved in .dev; foreign routes untouched.')
        elif args.command == 'status':
            run(COMPOSE + ['ps'])
            data = read(STATE / 'access.json')
            config = serve_status()
            for port, backend in ROUTES:
                print(f'Serve {port}: {"owned and active" if owned_route(config, data["dns"], port, backend) else "missing or changed"}')
        elif args.command == 'logs': run(COMPOSE + ['logs', '--tail', '80'])
        elif args.command == 'links':
            data = read(STATE / 'access.json')
            for role in ('meeting', 'host', 'viewer'):
                print(f"{role}: https://{data['dns']}/#invite={data[role]}")

if __name__ == '__main__':
    try: main()
    except (RuntimeError, OSError, subprocess.SubprocessError) as error:
        print(f'Checkpoint dev: {error}', file=sys.stderr)
        sys.exit(1)
