#!/usr/bin/env bash
# Standalone M0B installer. Download this file, inspect it, then run as root.
set -euo pipefail
command -v python3 >/dev/null || { echo 'Python 3 is required (Ubuntu 24.04 base package).' >&2; exit 1; }
exec python3 - "$@" <<'CHECKPOINT_SETUP_PY'
import argparse
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import pwd
import re
import secrets
import shutil
import socket
import ssl
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request
import urllib.error

LIVEKIT_VERSION = '1.13.11'
LIVEKIT_SHA256 = '1e9ee90ca301e1842b2887fc77cb122759756a9c8aeb4334ee91de3325ec43fb'
LIVEKIT_URL = f'https://github.com/livekit/livekit/releases/download/v{LIVEKIT_VERSION}/livekit_{LIVEKIT_VERSION}_linux_amd64.tar.gz'
STATE = Path('/etc/checkpoint/setup-state.json')
UNITS = ['checkpoint-livekit', 'checkpoint-core',
         'checkpoint-signal', 'checkpoint-observer', 'checkpoint-edge']
USERS = ['checkpoint', 'checkpoint-observer', 'checkpoint-livekit']


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(*args, capture=False):
    return subprocess.run(args, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None,
                          stderr=subprocess.PIPE if capture else None,
                          env={**os.environ, 'LC_ALL': 'C', 'DEBIAN_FRONTEND': 'noninteractive',
                               'NEEDRESTART_MODE': 'l'}).stdout


def digest(data):
    return hashlib.sha256(data).hexdigest()


def atomic(path, data, mode=0o600):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix='.checkpoint-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as out:
            os.fchmod(out.fileno(), mode)
            out.write(data)
            out.flush()
            os.fsync(out.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def archive_files(path, limit=300 * 1024 * 1024):
    # Never extract untrusted archive paths, links, devices or duplicate names.
    files = {}
    total = 0
    with tarfile.open(path, 'r:gz') as archive:
        for member in archive:
            name = member.name
            require(member.isfile() and not name.startswith('/') and
                    all(part not in ('', '.', '..') for part in name.split('/')) and
                    name not in files, f'Unsafe archive member: {name!r}')
            total += member.size
            require(total <= limit, 'Archive exceeds size limit')
            files[name] = archive.extractfile(member).read()
    return files


def bundle_files(path):
    require(Path(path).stat().st_size <= 300 * 1024 * 1024, 'Bundle too large')
    files = archive_files(path)
    manifest = json.loads(files.pop('manifest.json'))
    require(isinstance(manifest, dict) and set(manifest) == {'format', 'platform', 'commit', 'version', 'files'},
            'Invalid bundle manifest schema')
    require(manifest.get('format') == 1 and manifest.get('platform') == 'linux-amd64', 'Unsupported bundle')
    require(isinstance(manifest['commit'], str) and re.fullmatch('[a-f0-9]{40}', manifest['commit']), 'Bundle needs source commit')
    require(isinstance(manifest['version'], str) and re.fullmatch('[A-Za-z0-9._-]{1,80}', manifest['version']), 'Invalid build version')
    require(manifest.get('files') == {name: digest(data) for name, data in files.items()}, 'Bundle hashes do not match')
    require('checkpoint' in files and 'third_party/README.md' in files, 'Incomplete bundle')
    require(all(name in {'checkpoint', 'LICENSE', 'setup.sh', 'scripts/transcribe.py', 'scripts/minutes-assets.py', 'scripts/minutes-setup.py', 'deploy/minutes/models.lock.json', 'deploy/minutes/requirements.lock'} or name.startswith('third_party/') for name in files), 'Unexpected bundle member')
    binary = files['checkpoint']
    require(binary[:6] == b'\x7fELF\x02\x01' and binary[18:20] == b'\x3e\x00', 'Expected Linux amd64 ELF')
    return manifest, files


def arguments():
    parser = argparse.ArgumentParser(prog='setup.sh', description='Checkpoint setup: clean Ubuntu 24.04 amd64, native services. For existing installs use checkpoint update.')
    for name in ('app', 'rtc', 'turn', 'observer'):
        parser.add_argument('--' + name + '-domain', required=True)
    contact = parser.add_mutually_exclusive_group(required=True)
    contact.add_argument('--email', help='Certbot account email; --apply accepts the ACME terms')
    contact.add_argument('--no-email', action='store_true', help='Register Certbot without an email address')
    parser.add_argument('--public-ip', required=True, help='Public IPv4 assigned directly to this VPS (no NAT)')
    parser.add_argument('--ssh-port', type=int, default=22, help='SSH port to preserve in the host firewall')
    parser.add_argument('--bundle', required=True, help='Local artifact from scripts/build-native.sh')
    parser.add_argument('--livekit-archive', help='Optional offline copy of the pinned official archive')
    parser.add_argument('--apply', action='store_true', help='Install; without this flag, validate and show the plan only')
    return parser.parse_args()


def settings(args):
    domains = [getattr(args, name + '_domain') for name in ('app', 'rtc', 'turn', 'observer')]
    for domain in domains:
        require(len(domain) <= 253 and '.' in domain and all(
            re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label)
            for label in domain.split('.')), 'Use lowercase ASCII FQDNs without wildcards')
    require(len(set(domains)) == 4, 'Four distinct domain names are required')
    no_email = getattr(args, 'no_email', False)
    require(bool(args.email) != no_email, 'Choose either --email or --no-email')
    if not no_email:
        require(re.fullmatch(r'[A-Za-z0-9._+%-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}', args.email), 'Invalid ACME email')
    address = ipaddress.ip_address(args.public_ip)
    require(address.version == 4 and address.is_global, 'M0B requires a public IPv4')
    require(1 <= args.ssh_port <= 65535 and args.ssh_port not in
            (80, 443, 5349, 7880, 7881, 7882, 8443, 8444, 18180, 18181, 18182), 'Invalid/conflicting SSH port')
    return dict(zip(('app', 'rtc', 'turn', 'observer'), domains), email=args.email or '',
                ip=args.public_ip, ssh_port=args.ssh_port)


def certbot_contact(config):
    return ['--email', config['email']] if config['email'] else ['--register-unsafely-without-email']


def verify_owned(state):
    for name, expected in state.get('files', {}).items():
        path = Path(name)
        require(path.is_file() and not path.is_symlink() and digest(path.read_bytes()) == expected,
                f'Managed file changed or missing: {name}. Restore it before rerunning; setup will not overwrite edits.')
    for name, target in state.get('links', {}).items():
        require(Path(name).is_symlink() and os.readlink(name) == target, f'Managed link changed: {name}')


def preflight(config, state):
    require(os.geteuid() == 0, 'Run as root on the target VPS (also for complete preflight)')
    os_release = dict(line.split('=', 1) for line in Path('/etc/os-release').read_text().splitlines() if '=' in line)
    require(os_release.get('ID', '').strip('"') == 'ubuntu' and
            os_release.get('VERSION_ID', '').strip('"') == '24.04' and platform.machine() == 'x86_64',
            'Supported target: Ubuntu 24.04 LTS amd64 only')
    require(Path('/run/systemd/system').is_dir(), 'A booted systemd host is required')
    for command in ('ip', 'ss', 'systemctl', 'apt-get', 'useradd'):
        require(shutil.which(command), f'Missing base OS command: {command}')
    require(shutil.disk_usage('/').free >= 1024**3, 'At least 1 GiB free disk space is required')
    addresses = json.loads(run('ip', '-j', 'address', 'show', capture=True))
    require(any(a.get('local') == config['ip'] for interface in addresses for a in interface.get('addr_info', [])),
            'Public IPv4 must be assigned to this host; NAT is not supported in the M0B installer')
    for name in ('app', 'rtc', 'turn', 'observer'):
        found = {item[4][0] for item in socket.getaddrinfo(config[name], 443, type=socket.SOCK_STREAM)}
        require(found == {config['ip']}, f'DNS for {config[name]} must resolve only to {config["ip"]}; remove AAAA/proxy records')
    connection = os.environ.get('SSH_CONNECTION', '').split()
    require(not connection or (len(connection) == 4 and int(connection[3]) == config['ssh_port']),
            '--ssh-port must match the current SSH connection; firewall was not changed')
    if state:
        require(state['config'] == config, 'Installed target differs; M0B does not migrate domains, IPs or SSH ports')
        verify_owned(state)
    else:
        for path in ['/etc/checkpoint', '/opt/checkpoint', '/var/lib/checkpoint', '/var/lib/checkpoint-observer', '/usr/local/bin/checkpoint',
                     '/etc/nginx', '/etc/letsencrypt', '/usr/sbin/policy-rc.d',
                     '/etc/letsencrypt/renewal-hooks/deploy/checkpoint',
                     *['/etc/systemd/system/' + name + '.service' for name in UNITS]]:
            require(not os.path.lexists(path), f'Existing path conflicts with clean-host setup: {path}')
        for user in USERS:
            try:
                pwd.getpwnam(user)
            except KeyError:
                continue
            raise RuntimeError(f'Existing service user: {user}')
    ufw_managed = bool(state and state.get('ufw_managed'))
    if not ufw_managed:
        if shutil.which('ufw'):
            require('Status: inactive' in run('ufw', 'status', capture=True),
                    'Existing active UFW policy; review it before clean-host setup')
            added = run('ufw', 'show', 'added', capture=True)
            require(not any(line.startswith('ufw ') for line in added.splitlines()),
                    'Existing UFW user rules; refusing to replace policy')
        if shutil.which('nft'):
            tables = json.loads(run('nft', '-j', 'list', 'tables', capture=True)).get('nftables', [])
            require(not any('table' in item for item in tables), 'Existing firewall tables; no rules were changed')
        for command in ('iptables-save', 'ip6tables-save', 'iptables-legacy-save', 'ip6tables-legacy-save'):
            if shutil.which(command):
                rules = run(command, capture=True)
                require(not any(line.startswith('-A ') or re.match(r':\S+ (DROP|REJECT)', line)
                                for line in rules.splitlines()), 'Existing firewall rules; refusing to replace policy')
    for command in ('firewalld', 'nftables'):
        active = subprocess.run(['systemctl', 'is-enabled', command], capture_output=True, text=True)
        require(active.returncode != 0, f'{command} is enabled; disable/remove its policy explicitly before clean-host setup')
    # An occupied port is accepted only when every reported owner is our systemd service.
    ports = {80, 443, 5349, 7880, 7881, 7882, 8443, 8444, 18180, 18181, 18182}
    listeners = run('ss', '-H', '-lntup', capture=True).splitlines()
    require(any(line.split()[0] == 'tcp' and len(line.split()) > 4 and
                line.split()[4].rsplit(':', 1)[-1] == str(config['ssh_port']) for line in listeners),
            'No TCP listener on --ssh-port; refusing to install a firewall with an unverified administrative port')
    for line in listeners:
        columns = line.split()
        if len(columns) < 5 or not columns[4].rsplit(':', 1)[-1].isdigit():
            continue
        port = int(columns[4].rsplit(':', 1)[-1])
        if port not in ports:
            address = columns[4].rsplit(':', 1)[0].strip('[]')
            local = address == 'localhost' or address.startswith('127.') or address == '::1' or '%lo' in address
            require(state or local or port in (config['ssh_port'], 68, 546),
                    f'Existing non-local service on port {port}; this is not a clean VPS')
            continue
        pids = re.findall(r'pid=(\d+)', line)
        require(state and pids and all(any('/' + unit + '.service' in Path('/proc/' + pid + '/cgroup').read_text()
                                          for unit in UNITS) for pid in pids), f'Port {port} has a foreign listener')


def nginx_config(c, tls=True):
    cert = '/etc/letsencrypt/live/checkpoint/fullchain.pem'
    key = '/etc/letsencrypt/live/checkpoint/privkey.pem'
    ssl = f'ssl_certificate {cert};\nssl_certificate_key {key};\nssl_protocols TLSv1.2 TLSv1.3;'
    stream = ''
    if tls:
        # Carry client addresses through both stream hops. LiveKit requires a
        # trusted PROXY header so Firefox does not see a loopback mapped address.
        stream = f'''
stream {{
    map $ssl_preread_server_name $checkpoint_upstream {{
        {c['app']} 127.0.0.1:8443;
        {c['rtc']} 127.0.0.1:8443;
        {c['observer']} 127.0.0.1:8443;
        {c['turn']} 127.0.0.1:8444;
        default 127.0.0.1:9;
    }}
    server {{
        listen {c['ip']}:443;
        ssl_preread on;
        proxy_pass $checkpoint_upstream;
        proxy_protocol on;
        proxy_connect_timeout 5s;
        proxy_timeout 1d;
    }}
    server {{
        listen 127.0.0.1:8444 ssl proxy_protocol;
        set_real_ip_from 127.0.0.1;
        {ssl}
        proxy_pass 127.0.0.1:5349;
        proxy_protocol on;
        proxy_timeout 1d;
    }}
}}
'''
    servers = ''
    if tls:
        for name, port in [('app', 18180), ('rtc', 18181), ('observer', 18182)]:
            location = 'location /' if name != 'rtc' else 'location ~ ^/rtc(?:/v1)?(?:/validate)?$'
            servers += f'''
    server {{
        listen 127.0.0.1:8443 ssl proxy_protocol;
        server_name {c[name]};
        set_real_ip_from 127.0.0.1;
        real_ip_header proxy_protocol;
        {ssl}
        {'location / { return 404; }' if name == 'rtc' else ''}
        {location} {{
            proxy_pass http://127.0.0.1:{port};
            proxy_http_version 1.1;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $remote_addr;
            proxy_set_header X-Forwarded-Proto https;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection $checkpoint_connection;
            proxy_read_timeout 1d;
            proxy_send_timeout 1d;
            proxy_buffering off;
        }}
    }}
'''
    return f'''# Managed by Checkpoint M0B setup. Separate from the distro nginx.service.
load_module /usr/lib/nginx/modules/ngx_stream_module.so;
user www-data;
worker_processes auto;
pid /run/checkpoint-edge.pid;
error_log /var/log/checkpoint-edge.log crit;
events {{ worker_connections 4096; }}
{stream}
http {{
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    access_log off;
    server_tokens off;
    client_max_body_size 160k;
    map $http_upgrade $checkpoint_connection {{ default upgrade; '' close; }}
    server {{
        listen {c['ip']}:80 default_server;
        server_name _;
        location ^~ /.well-known/acme-challenge/ {{ root /var/lib/checkpoint-acme; }}
        location / {{ return 404; }}
    }}
    {servers}
}}
'''


def livekit_config(c, s):
    return f'''port: 7880
bind_addresses: ["127.0.0.1"]
rtc:
  node_ip: "{c['ip']}"
  use_external_ip: false
  tcp_port: 7881
  udp_port: 7882
  port_range_start: 0
  port_range_end: 0
  enable_loopback_candidate: false
  use_ice_lite: true
turn:
  enabled: true
  domain: "{c['turn']}"
  tls_port: 5349
  udp_port: 0
  external_tls: true
  bind_addresses: ["0.0.0.0"]
  proxy_protocol: true
  proxy_protocol_trusted_cidrs: ["127.0.0.1/32"]
  relay_range_start: 30000
  relay_range_end: 30100
keys:
  {s['api_key']}: "{s['api_secret']}"
webhook:
  api_key: "{s['api_key']}"
  urls: ["http://127.0.0.1:18180/api/livekit/webhook"]
room:
  max_participants: 200
  empty_timeout: 300
logging:
  level: warn
'''


UFW_FILES = ('/etc/ufw/user.rules', '/etc/ufw/user6.rules', '/etc/ufw/ufw.conf', '/etc/default/ufw',
             '/etc/ufw/before.rules', '/etc/ufw/before6.rules', '/etc/ufw/after.rules', '/etc/ufw/after6.rules')


def firewall_commands(c):
    policy = Path('/etc/checkpoint/deploy-policy.json')
    commands = [('ufw', 'default', 'deny', 'incoming'), ('ufw', 'default', 'allow', 'outgoing'),
                ('ufw', 'default', 'deny', 'routed')]
    if policy.exists():
        saved = json.loads(policy.read_text())
        require(saved['sshPort'] == c['ssh_port'], 'Deployment SSH port differs')
        networks = [ipaddress.ip_network(value, strict=True) for value in saved['allowedCIDRs']]
        require(networks, 'Empty deployment SSH allowlist')
        for network in networks:
            commands.append(('ufw', 'allow', 'proto', 'tcp', 'from', str(network), 'to', 'any',
                             'port', str(c['ssh_port']), 'comment', 'Checkpoint SSH'))
    else:
        commands.append(('ufw', 'allow', str(c['ssh_port']) + '/tcp', 'comment', 'Checkpoint SSH'))
    for port in (80, 443, 7881):
        commands.append(('ufw', 'allow', str(port) + '/tcp', 'comment', 'Checkpoint'))
    commands.append(('ufw', 'allow', '7882/udp', 'comment', 'Checkpoint media'))
    commands.append(('ufw', '--force', 'enable'))
    return commands


def configure_firewall(c, installer=None):
    if installer:
        installer.state['ufw_managed'] = True
        installer.save()
    defaults = Path('/etc/default/ufw')
    body = defaults.read_text()
    require(re.search(r'^IPV6=(yes|no)$', body, re.M), 'UFW IPV6 setting missing')
    if re.search(r'^IPV6=no$', body, re.M):
        atomic(defaults, re.sub(r'^IPV6=no$', 'IPV6=yes', body, flags=re.M).encode(), 0o644)
    for command in firewall_commands(c):
        run(*command)
    if installer:
        for path in UFW_FILES:
            installer.state['files'][path] = digest(Path(path).read_bytes())
        installer.save()


def service(command, user, extra=''):
    return f'''[Unit]
Description=Checkpoint {command}
Wants=network-online.target
After=network-online.target ufw.service
Requires=ufw.service

[Service]
Type=simple
User={user}
Group={user}
ExecStart=/opt/checkpoint/current/checkpoint {command}
Restart=on-failure
RestartSec=3
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
# Go/LiveKit enumerates network interfaces through netlink; capabilities remain empty.
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
CapabilityBoundingSet=
LimitNOFILE=65536
{extra}
[Install]
WantedBy=multi-user.target
'''


def generated(c, s):
    shared = f'''LIVEKIT_URL=http://127.0.0.1:7880
LIVEKIT_API_KEY={s['api_key']}
LIVEKIT_API_SECRET={s['api_secret']}
'''
    core_env = shared + f'''CHECKPOINT_PUBLIC_URL=https://{c['app']}
CHECKPOINT_SIGNAL_URL=wss://{c['rtc']}
CHECKPOINT_OBSERVER_URL=https://{c['observer']}
CHECKPOINT_DATABASE=/var/lib/checkpoint/checkpoint.db
'''
    observer_env = shared + 'CHECKPOINT_CORE_HEALTH_URL=http://127.0.0.1:18180/healthz\nCHECKPOINT_OBSERVER_DATABASE=/var/lib/checkpoint-observer/observer.db\n'
    units = {
        'core': service('serve', 'checkpoint', 'EnvironmentFile=/etc/checkpoint/core.env\nReadWritePaths=/var/lib/checkpoint'),
        # Same UID for the private SQLite DB; signal opens mode=ro; systemd mounts it read-only here.
        'signal': service('signal', 'checkpoint', 'EnvironmentFile=/etc/checkpoint/core.env\nReadOnlyPaths=/var/lib/checkpoint'),
        'observer': service('observe', 'checkpoint-observer', 'EnvironmentFile=/etc/checkpoint/observer.env\nInaccessiblePaths=/var/lib/checkpoint\nReadWritePaths=/var/lib/checkpoint-observer'),
        'livekit': service('livekit', 'checkpoint-livekit').replace(
            'ExecStart=/opt/checkpoint/current/checkpoint livekit',
            'ExecStart=/opt/checkpoint/livekit/' + LIVEKIT_VERSION + '/livekit-server --config /etc/checkpoint/livekit.yaml'),
        'edge': '''[Unit]
Description=Checkpoint HTTPS, WSS and TURN TLS edge
Wants=network-online.target
After=network-online.target ufw.service
Requires=ufw.service

[Service]
Type=simple
ExecStartPre=/usr/sbin/nginx -t -q -c /etc/checkpoint/nginx.conf
ExecStart=/usr/sbin/nginx -c /etc/checkpoint/nginx.conf -g "daemon off;"
ExecReload=/bin/sh -c '/usr/sbin/nginx -t -q -c /etc/checkpoint/nginx.conf && /bin/kill -HUP $MAINPID'
KillSignal=SIGQUIT
TimeoutStopSec=10
Restart=on-failure
RestartSec=3
PrivateTmp=true
ProtectHome=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
''',
    }
    files = {
        '/etc/checkpoint/core.env': (core_env, 0o600),
        '/etc/checkpoint/observer.env': (observer_env, 0o600),
        '/etc/checkpoint/livekit.yaml': (livekit_config(c, s), 0o640),
        '/etc/letsencrypt/renewal-hooks/deploy/checkpoint': ('''#!/bin/sh
set -eu
# Graceful Nginx reload only. LiveKit and established calls keep running.
/usr/sbin/nginx -t -q -c /etc/checkpoint/nginx.conf
/usr/bin/systemctl reload checkpoint-edge.service
''', 0o755),
    }
    for name, unit in units.items():
        files[f'/etc/systemd/system/checkpoint-{name}.service'] = (unit, 0o644)
    return files


class Installer:
    def __init__(self, state):
        self.state = state

    def save(self):
        atomic(STATE, (json.dumps(self.state, indent=2) + '\n').encode())

    def write(self, name, data, mode=0o600):
        data = data.encode() if isinstance(data, str) else data
        path = Path(name)
        known = self.state['files'].get(name)
        if os.path.lexists(path):
            require(known and not path.is_symlink() and digest(path.read_bytes()) == known, f'Foreign/edited file: {name}')
        if known != digest(data):
            atomic(path, data, mode)
        os.chmod(path, mode)
        self.state['files'][name] = digest(data)
        self.save()

    def link(self, name, target):
        if os.path.lexists(name):
            require(self.state['links'].get(name) == target and Path(name).is_symlink() and
                    os.readlink(name) == target, f'Foreign link: {name}')
        else:
            Path(name).parent.mkdir(parents=True, exist_ok=True)
            os.symlink(target, name)
        self.state['links'][name] = target
        self.save()


def install_packages():
    # Do not let distro nginx bind :80 or replace an existing service policy.
    policy = Path('/usr/sbin/policy-rc.d')
    require(not policy.exists(), 'Existing package service policy; refusing to replace it')
    atomic(policy, b'#!/bin/sh\nexit 101\n', 0o755)
    try:
        run('apt-get', 'update')
        run('apt-get', 'install', '-y', '--no-install-recommends', 'nginx', 'libnginx-mod-stream',
            'certbot', 'ca-certificates', 'ufw')
    finally:
        policy.unlink()
    run('systemctl', 'disable', 'nginx.service')


def check_health(c):
    for port in (18180, 18181, 18182):
        for attempt in range(30):
            try:
                with urllib.request.urlopen(f'http://127.0.0.1:{port}/healthz', timeout=2) as response:
                    require(response.status == 200, 'Health check failed')
                break
            except Exception:
                if attempt == 29:
                    raise RuntimeError(f'Health check failed on {port}; inspect journalctl -u checkpoint-*')
                time.sleep(1)
    for name in ('app', 'observer'):
        with urllib.request.urlopen('https://' + c[name], timeout=10) as response:
            require(response.status == 200, f'HTTPS check failed for {name}')
    for path, expected in [('/rtc/validate', 401), ('/twirp/livekit.RoomService/ListRooms', 404)]:
        try:
            with urllib.request.urlopen('https://' + c['rtc'] + path, timeout=10) as response:
                status = response.status
        except urllib.error.HTTPError as error:
            status = error.code
        require(status == expected, 'Unexpected public signaling route response')
    with socket.create_connection((c['turn'], 443), timeout=10) as transport:
        with ssl.create_default_context().wrap_socket(transport, server_hostname=c['turn']):
            pass
    for attempt in range(20):
        with urllib.request.urlopen('http://127.0.0.1:18182/api/health', timeout=3) as response:
            health = json.load(response)
        if all(health.get(key, {}).get('state') == 'operational' for key in ('core', 'signaling')):
            break
        require(attempt < 19, 'Observer could not verify core and authenticated LiveKit API')
        time.sleep(1)


def main():
    os.umask(0o077)
    args = arguments()
    config = settings(args)
    manifest, files = bundle_files(args.bundle)
    state = json.loads(STATE.read_text()) if STATE.exists() else None
    preflight(config, state)
    bundle_hash = digest(Path(args.bundle).read_bytes())
    require(not state or state['bundle_sha256'] == bundle_hash,
            'Different bundle: M0B setup is not an updater. Use the original artifact; update belongs to M5.')
    print('Plan: Ubuntu packages; 3 service users; 4 application services; Nginx edge; Certbot; UFW host firewall.')
    print(f"Allow inbound TCP {config['ssh_port']},80,443,7881 and UDP 7882; preserve established connections and ICMP.")
    print('Provider firewall/DNS must already allow these ports. IPv4 only; no NAT, CDN proxy or existing web/firewall configuration.')
    print(f"Build: {manifest['version']} / {manifest['commit']}. Locally supplied bundle; use install.sh for signature verification.")
    if not args.apply:
        print('Preflight passed. Rerun with --apply to install and accept the ACME subscriber agreement.')
        return
    # Retain the descriptor for the complete install; serialize competing runs.
    with open('/run/checkpoint-setup.lock', 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        state = json.loads(STATE.read_text()) if STATE.exists() else None
        preflight(config, state)
        require(not state or state['bundle_sha256'] == bundle_hash, 'Another install selected a different bundle')
        with tempfile.TemporaryDirectory(prefix='checkpoint-setup-') as temporary:
            archive = Path(args.livekit_archive) if args.livekit_archive else Path(temporary) / 'livekit.tar.gz'
            if not args.livekit_archive:
                with urllib.request.urlopen(LIVEKIT_URL, timeout=60) as response, archive.open('wb') as out:
                    shutil.copyfileobj(response, out)
            require(digest(archive.read_bytes()) == LIVEKIT_SHA256, 'LiveKit archive checksum mismatch')
            livekit = archive_files(archive)
            require(set(livekit) == {'livekit-server', 'LICENSE'}, 'Unexpected upstream archive contents')
        if state is None:
            state = dict(format=1, config=config, bundle_sha256=bundle_hash, manifest=manifest,
                         secrets={key: secrets.token_hex(32) for key in ('api_key', 'api_secret')},
                         files={}, links={}, packages=False, complete=False)
            Path('/etc/checkpoint').mkdir(mode=0o755)
        # mkdir's mode is filtered by the installation umask; LiveKit must be
        # able to traverse this directory to read its group-readable config.
        os.chmod('/etc/checkpoint', 0o755)
        installer = Installer(state)
        installer.save()
        if not state['packages']:
            install_packages()
            state['packages'] = True
            installer.save()
        for user in USERS:
            try:
                pwd.getpwnam(user)
            except KeyError:
                run('useradd', '--system', '--user-group', '--no-create-home', '--home-dir', '/nonexistent',
                    '--shell', '/usr/sbin/nologin', user)
        for name, user, mode in [('/var/lib/checkpoint', 'checkpoint', 0o700),
                                 ('/var/lib/checkpoint-observer', 'checkpoint-observer', 0o700),
                                 ('/var/lib/checkpoint-acme', 'root', 0o755)]:
            Path(name).mkdir(parents=True, exist_ok=True)
            os.chmod(name, mode)
            account = pwd.getpwnam(user)
            os.chown(name, account.pw_uid, account.pw_gid)
        release = '/opt/checkpoint/releases/' + manifest['version'] + '-' + manifest['files']['checkpoint'][:12]
        for name, data in files.items():
            installer.write(release + '/' + name, data, 0o755 if name == 'checkpoint' else 0o644)
        for name, data in livekit.items():
            installer.write('/opt/checkpoint/livekit/' + LIVEKIT_VERSION + '/' + name, data,
                            0o755 if name == 'livekit-server' else 0o644)
        # mkstemp respects explicit file modes; intermediate directories must be traversable.
        for base in ('/opt/checkpoint', '/etc/letsencrypt/renewal-hooks'):
            if Path(base).exists():
                for directory, _, _ in os.walk(base):
                    os.chmod(directory, 0o755)
        installer.link('/opt/checkpoint/current', release)
        installer.link('/usr/local/bin/checkpoint', '/opt/checkpoint/current/checkpoint')
        for name, (data, mode) in generated(config, state['secrets']).items():
            installer.write(name, data, mode)
        # LiveKit reads its own config; systemd reads the root-only env files.
        os.chown('/etc/checkpoint/livekit.yaml', 0, pwd.getpwnam('checkpoint-livekit').pw_gid)
        for directory in ('/etc/letsencrypt/renewal-hooks', '/etc/letsencrypt/renewal-hooks/deploy'):
            os.chmod(directory, 0o755)
        run('systemctl', 'daemon-reload')
        configure_firewall(config, installer)
        run('systemctl', 'enable', '--now', 'ufw.service')
        for name in UNITS[:4]:
            run('systemctl', 'enable', '--now', name + '.service')
        certificate = Path('/etc/letsencrypt/live/checkpoint/fullchain.pem')
        if not certificate.exists():
            installer.write('/etc/checkpoint/nginx.conf', nginx_config(config, tls=False), 0o644)
            run('systemctl', 'enable', '--now', 'checkpoint-edge.service')
            run('certbot', 'certonly', '--non-interactive', '--agree-tos', *certbot_contact(config),
                '--webroot', '-w', '/var/lib/checkpoint-acme', '--cert-name', 'checkpoint',
                *[part for name in ('app', 'rtc', 'turn', 'observer') for part in ('-d', config[name])])
        installer.write('/etc/checkpoint/nginx.conf', nginx_config(config), 0o644)
        run('nginx', '-t', '-q', '-c', '/etc/checkpoint/nginx.conf')
        run('systemctl', 'enable', '--now', 'checkpoint-edge.service', 'certbot.timer')
        run('systemctl', 'reload', 'checkpoint-edge.service')
        check_health(config)
        state['complete'] = True
        installer.save()
        print(f"App: https://{config['app']}\nObserver: https://{config['observer']}")
        run('systemctl', 'is-active', *[name + '.service' for name in UNITS], 'ufw.service', 'certbot.timer')
        print('Invitations: /etc/checkpoint/core.env (root only); never paste this file into reports.')
        print('Diagnostics: systemctl status checkpoint-*; journalctl -u checkpoint-core -u checkpoint-signal -u checkpoint-livekit -u checkpoint-edge')
        print('Next: certbot renew --dry-run --run-deploy-hooks; reboot; two external networks; selected ICE relay over TURN/TLS 443.')
        print('A successful local check does not establish public media reachability or complete M0B acceptance.')


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, subprocess.CalledProcessError, tarfile.TarError) as error:
        print(f'Setup stopped: {error}', file=sys.stderr)
        print('No data is deleted. Fix the reported condition and rerun the same artifact and arguments.', file=sys.stderr)
        sys.exit(1)
CHECKPOINT_SETUP_PY
