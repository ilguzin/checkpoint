"""Run real native binaries and generated edge config in an isolated Ubuntu container.

No systemd PID 1, public CA or host mutation: those remain VPS acceptance.
Firewall rules are tested only inside this container's private network namespace.
"""
import json
import os
from pathlib import Path
import pwd
import signal
import socket
import subprocess
import time

from setup_test import load_setup

setup = load_setup()
root = Path('/work/.dev/native')
ip = socket.gethostbyname(socket.gethostname())
c = dict(app='meet.checkpoint.test', rtc='rtc.checkpoint.test', turn='turn.checkpoint.test',
         observer='status.checkpoint.test', ip=ip, email='test@example.com', ssh_port=22)
s = {name: setup.secrets.token_hex(32) for name in ('api_key', 'api_secret', 'meeting', 'host', 'viewer')}
Path('/etc/checkpoint').mkdir()
for user in setup.USERS:
    setup.run('useradd', '--system', '--user-group', '--no-create-home', user)
Path('/var/lib/checkpoint').mkdir(mode=0o700)
account = pwd.getpwnam('checkpoint')
os.chown('/var/lib/checkpoint', account.pw_uid, account.pw_gid)
Path('/var/lib/checkpoint-observer').mkdir(mode=0o700)
observer_account = pwd.getpwnam('checkpoint-observer')
os.chown('/var/lib/checkpoint-observer', observer_account.pw_uid, observer_account.pw_gid)
Path('/var/lib/checkpoint-acme').mkdir(mode=0o755)
for name, (data, mode) in setup.generated(c, s).items():
    if name.endswith('core.env'):
        data += f"CHECKPOINT_MEETING_INVITE={s['meeting']}\nCHECKPOINT_HOST_INVITE={s['host']}\nCHECKPOINT_VIEWER_INVITE={s['viewer']}\n"
    if name.endswith('livekit.yaml'):
        # Docker test IP is private; production only accepts globally routed IPs.
        data = data.replace('  external_tls: true', '  external_tls: true\n  allow_restricted_peer_cidrs: ["' + ip + '/32"]')
    setup.atomic(name, data.encode(), mode)
os.chown('/etc/checkpoint/livekit.yaml', 0, pwd.getpwnam('checkpoint-livekit').pw_gid)
Path('/etc/letsencrypt/live/checkpoint').mkdir(parents=True)
setup.run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
          '-subj', '/CN=checkpoint.test', '-addext', 'subjectAltName=' + ','.join(
              'DNS:' + c[name] for name in ('app', 'rtc', 'turn', 'observer')),
          '-keyout', '/etc/letsencrypt/live/checkpoint/privkey.pem',
          '-out', '/etc/letsencrypt/live/checkpoint/fullchain.pem')
# The .test names are local to this container and the browser test container.
with open('/etc/hosts', 'a') as hosts:
    hosts.write('\n' + ip + ' ' + ' '.join(c[n] for n in ('app', 'rtc', 'turn', 'observer')) + '\n')
manifest, bundle = setup.bundle_files(root / 'checkpoint-linux-amd64.tar.gz')
setup.atomic('/opt/checkpoint/current/checkpoint', bundle['checkpoint'], 0o755)
for directory in ('/opt/checkpoint', '/opt/checkpoint/current'):
    os.chmod(directory, 0o755)
upstream = setup.archive_files(root / 'livekit.tar.gz')
setup.require(setup.digest((root / 'livekit.tar.gz').read_bytes()) == setup.LIVEKIT_SHA256, 'Bad LiveKit hash')
setup.atomic('/opt/checkpoint/livekit/' + setup.LIVEKIT_VERSION + '/livekit-server', upstream['livekit-server'], 0o755)
for directory in ('/opt/checkpoint/livekit', '/opt/checkpoint/livekit/' + setup.LIVEKIT_VERSION):
    os.chmod(directory, 0o755)
setup.atomic('/etc/checkpoint/nginx.conf', setup.nginx_config(c).encode(), 0o644)
setup.atomic(root / 'test-ca.pem', Path('/etc/letsencrypt/live/checkpoint/fullchain.pem').read_bytes(), 0o644)
setup.run('nginx', '-t', '-c', '/etc/checkpoint/nginx.conf')
setup.run('systemd-analyze', 'verify', *['/etc/systemd/system/' + unit + '.service' for unit in setup.UNITS])
# NET_ADMIN is granted only in this container's network namespace. Exercise the
# real rules twice to catch syntax and duplicate-chain problems on reapply.
# Docker exposes /proc/sys read-only; retain the namespace kernel defaults.
# Production UFW uses its normal sysctl configuration.
ufw_defaults = Path('/etc/default/ufw')
ufw_defaults.write_text(ufw_defaults.read_text().replace('IPT_SYSCTL=/etc/ufw/sysctl.conf', 'IPT_SYSCTL=/dev/null'))
setup.configure_firewall(c)
setup.configure_firewall(c)
# Exercise the same UFW rule narrowing and file rollback as VPS hardening.
ufw_snapshot = {name: Path(name).read_bytes() for name in setup.UFW_FILES}
setup.run('ufw', 'allow', 'proto', 'tcp', 'from', '198.51.100.10/32', 'to', 'any', 'port', '22', 'comment', 'Checkpoint SSH')
setup.run('ufw', '--force', 'delete', 'allow', '22/tcp')
setup.require('ufw allow 22/tcp' not in setup.run('ufw', 'show', 'added', capture=True), 'Broad SSH rule survived narrowing')
for name, body in ufw_snapshot.items():
    Path(name).write_bytes(body)
setup.run('ufw', 'reload')
setup.require('ufw allow 22/tcp' in setup.run('ufw', 'show', 'added', capture=True), 'UFW rollback did not restore SSH')


def env_file(path):
    return dict(line.split('=', 1) for line in Path(path).read_text().splitlines())


def launch(user, args, env=None):
    account = pwd.getpwnam(user)
    return subprocess.Popen(args, env={**os.environ, **(env or {})}, user=account.pw_uid,
                            group=account.pw_gid, extra_groups=[])


# Initialize an isolated test administrator before starting read-only signal.
s['adminPassword'] = setup.secrets.token_hex(24)
password_file = Path('/var/lib/checkpoint/test-admin-password')
setup.atomic(password_file, s['adminPassword'].encode(), 0o600)
account = pwd.getpwnam('checkpoint')
os.chown(password_file, account.pw_uid, account.pw_gid)
bootstrap = launch('checkpoint', ['/opt/checkpoint/current/checkpoint', 'admin', 'init',
                                  '--username', 'admin', '--password-file', str(password_file)],
                   env_file('/etc/checkpoint/core.env'))
setup.require(bootstrap.wait(timeout=30) == 0, 'Admin bootstrap failed')
password_file.unlink()

# Second ordinary account is created only through the local CLI.
s['secondUsername'] = 'alex'
s['secondPassword'] = setup.secrets.token_hex(24)
setup.atomic(password_file, s['secondPassword'].encode(), 0o600)
os.chown(password_file, account.pw_uid, account.pw_gid)
second = launch('checkpoint', ['/opt/checkpoint/current/checkpoint', 'users', 'add',
                              '--username', s['secondUsername'], '--password-file', str(password_file)],
                env_file('/etc/checkpoint/core.env'))
setup.require(second.wait(timeout=30) == 0, 'Second account creation failed')
password_file.unlink()

processes = [launch('checkpoint-livekit', ['/opt/checkpoint/livekit/' + setup.LIVEKIT_VERSION + '/livekit-server',
                                         '--config', '/etc/checkpoint/livekit.yaml'])]
for cmd, user, file in [('serve', 'checkpoint', 'core.env'), ('signal', 'checkpoint', 'core.env'),
                         ('observe', 'checkpoint-observer', 'observer.env')]:
    processes.append(launch(user, ['/opt/checkpoint/current/checkpoint', cmd], env_file('/etc/checkpoint/' + file)))
processes.append(subprocess.Popen(['nginx', '-c', '/etc/checkpoint/nginx.conf', '-g', 'daemon off;']))
# Only the private test directory receives invitations. Do not copy into reports.
setup.atomic(root / 'access.json', json.dumps(dict(c, **s, publicURL='https://' + c['app'],
                                                observerURL='https://' + c['observer'])).encode(), 0o600)
os.chown(root / 'access.json', int(os.environ['TEST_UID']), int(os.environ['TEST_GID']))


def stop(*_):
    for process in processes:
        process.terminate()
    raise SystemExit(0)


signal.signal(signal.SIGTERM, stop)
while True:
    setup.require(all(process.poll() is None for process in processes), 'Native process exited; see container logs')
    restart = root / 'restart-request'
    if restart.exists():
        command, nonce = restart.read_text().split(':', 1)
        restart.unlink()
        setup.require(command in ('core', 'signal'), 'Unsupported test restart')
        index = 1 if command == 'core' else 2
        processes[index].terminate()
        processes[index].wait(timeout=10)
        # Keep media and Observer alive while the control process is unavailable.
        time.sleep(12 if command == 'core' else 2)
        processes[index] = launch('checkpoint', ['/opt/checkpoint/current/checkpoint', 'serve' if command == 'core' else 'signal'], env_file('/etc/checkpoint/core.env'))
        setup.atomic(root / 'restart-done', nonce.encode(), 0o644)
    request = root / 'reload-request' 
    if request.exists():
        nonce = request.read_text()
        request.unlink()
        setup.run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                  '-subj', '/CN=checkpoint.test', '-addext', 'subjectAltName=DNS:*.checkpoint.test',
                  '-keyout', '/etc/letsencrypt/live/checkpoint/privkey.pem',
                  '-out', '/etc/letsencrypt/live/checkpoint/fullchain.pem')
        setup.run('nginx', '-t', '-c', '/etc/checkpoint/nginx.conf')
        setup.run('nginx', '-s', 'reload', '-c', '/etc/checkpoint/nginx.conf')
        setup.atomic(root / 'test-ca.pem', Path('/etc/letsencrypt/live/checkpoint/fullchain.pem').read_bytes(), 0o644)
        setup.atomic(root / 'reload-done', nonce.encode(), 0o644)
    time.sleep(1)
