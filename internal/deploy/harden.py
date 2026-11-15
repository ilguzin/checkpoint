import base64, fcntl, hashlib, ipaddress, json, os, subprocess, sys, tempfile
from pathlib import Path

os.umask(0o077)
ROOT = Path('/var/lib/checkpoint-deploy')
STATE = ROOT / 'transaction.json'
SETUP_STATE = '/etc/checkpoint/setup-state.json'
UFW_FILES = ['/etc/ufw/user.rules', '/etc/ufw/user6.rules', '/etc/ufw/ufw.conf', '/etc/default/ufw',
             '/etc/ufw/before.rules', '/etc/ufw/before6.rules', '/etc/ufw/after.rules', '/etc/ufw/after6.rules']
FILES = ['/etc/ssh/sshd_config.d/00-checkpoint.conf', SETUP_STATE, '/etc/checkpoint/deploy-policy.json', *UFW_FILES]

def require(ok, message):
    if not ok: raise RuntimeError(message)

def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout

def atomic(path, data, mode=0o600):
    path = Path(path)
    fd, name = tempfile.mkstemp(prefix='.checkpoint-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data); stream.flush(); os.fsync(stream.fileno())
        os.chmod(name, mode); os.replace(name, path)
    finally:
        Path(name).unlink(missing_ok=True)

def rollback():
    saved = json.loads(STATE.read_text())
    for name, value in saved['files'].items():
        if value is None:
            Path(name).unlink(missing_ok=True)
        else:
            atomic(name, base64.b64decode(value['body']), value['mode'])
    run('sshd', '-t'); run('systemctl', 'reload', 'ssh'); run('ufw', 'reload')
    STATE.unlink()

require(os.geteuid() == 0, 'Root required on target')
# Serialize stage, automatic rollback and commit, including timer races.
ROOT.mkdir(mode=0o700, exist_ok=True)
lock = (ROOT / 'lock').open('a')
fcntl.flock(lock, fcntl.LOCK_EX)
action = sys.argv[1]
if action == 'rollback':
    if STATE.exists(): rollback()
    raise SystemExit(0)
if action == 'commit':
    if not STATE.exists():
        raise RuntimeError('No pending security transaction; it may have rolled back. Run deploy apply again.')
    run('systemctl', 'stop', 'checkpoint-security-rollback.timer')
    STATE.unlink(missing_ok=True)
    print('Second SSH connection verified; security rollback cancelled.')
    raise SystemExit(0)
require(action == 'stage', 'Unknown hardening action')
policy = json.load(sys.stdin)
peer = ipaddress.ip_address(os.environ['SSH_CONNECTION'].split()[0])
networks = [ipaddress.ip_network(cidr, strict=True) for cidr in policy['allowedCIDRs']]
require(any(peer in network for network in networks if peer.version == network.version), 'Operator address is outside allowedCIDRs')
# Install security updates and private daily backups before changing SSH.
run('apt-get', 'update')
run('apt-get', 'install', '-y', '--no-install-recommends', 'unattended-upgrades')
def managed(path, data, mode=0o644):
    path = Path(path)
    require(not path.is_symlink(), 'Managed path is a symlink')
    require(not path.exists() or path.read_bytes() == data, 'Existing managed configuration differs; review it first')
    if not path.exists(): atomic(path, data, mode)
managed('/etc/apt/apt.conf.d/52checkpoint-security', b'// Managed by Checkpoint deployment\nUnattended-Upgrade::Allowed-Origins { "${distro_id}:${distro_codename}-security"; };\nUnattended-Upgrade::Automatic-Reboot "false";\nAPT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";\n')
backups = Path('/var/backups/checkpoint')
require(not backups.is_symlink(), 'Backup directory is a symlink')
backups.mkdir(mode=0o700, exist_ok=True)
backup_script = ("""# Managed by Checkpoint deployment
import datetime, os, pathlib, re, subprocess, time
os.umask(0o077)
root = pathlib.Path('/var/backups/checkpoint')
name = 'checkpoint-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '.tar.gz'
subprocess.run(['/opt/checkpoint/current/checkpoint', 'backup', '--output', str(root / name), '--config-dir', '/etc/checkpoint'], check=True)
for path in root.iterdir():
    if re.fullmatch(r'checkpoint-[0-9]{8}T[0-9]{6}Z[.]tar[.]gz', path.name) and path.is_file() and not path.is_symlink() and path.stat().st_mtime < time.time() - RETENTION * 86400:
        path.unlink()
""").replace('RETENTION', str(policy['retentionDays'])).encode()
managed('/etc/checkpoint/backup.py', backup_script, 0o700)
managed('/etc/systemd/system/checkpoint-backup.service', b'[Unit]\nDescription=Checkpoint private backup\nConditionPathExists=/var/lib/checkpoint/checkpoint.db\n[Service]\nType=oneshot\nEnvironment=CHECKPOINT_DATABASE=/var/lib/checkpoint/checkpoint.db\nExecStart=/usr/bin/python3 /etc/checkpoint/backup.py\nUMask=0077\nNoNewPrivileges=true\nPrivateTmp=true\n')
managed('/etc/systemd/system/checkpoint-backup.timer', b'[Unit]\nDescription=Daily Checkpoint backup\n[Timer]\nOnCalendar=daily\nRandomizedDelaySec=900\nPersistent=true\n[Install]\nWantedBy=timers.target\n')
run('systemctl', 'daemon-reload')
run('systemctl', 'enable', '--now', 'checkpoint-backup.timer', 'apt-daily.timer', 'apt-daily-upgrade.timer')

ROOT.mkdir(mode=0o700, exist_ok=True)
require(not STATE.exists(), 'Unfinished security change: run deploy recover first')
ssh = Path(FILES[0])
require(not ssh.exists() or ssh.read_text().startswith('# Managed by Checkpoint deployment\n'), 'Foreign SSH drop-in exists')
port = policy['sshPort']
metadata = Path('/etc/checkpoint/deploy-policy.json')
setup = json.loads(Path(SETUP_STATE).read_text())
require(setup.get('ufw_managed') and 'Status: active' in run('ufw', 'status'), 'Managed active UFW required')
for name in UFW_FILES:
    require(setup['files'].get(name) == hashlib.sha256(Path(name).read_bytes()).hexdigest(),
            'UFW configuration changed; review before deploying: ' + name)
identical = metadata.exists() and json.loads(metadata.read_text()) == policy
require(not metadata.exists() or identical, 'Existing SSH policy differs; review before replacing it')
saved = {'files': {name: ({'body': base64.b64encode(Path(name).read_bytes()).decode(), 'mode': Path(name).stat().st_mode & 0o777} if Path(name).exists() else None) for name in FILES}}
atomic(STATE, json.dumps(saved).encode())
# The rollback script is placed in persistent root-private storage, so SSH loss
# or removal of temporary deployment files does not disable the timer.
script = ROOT / 'harden.py'
if Path(__file__).resolve() != script:
    atomic(script, Path(__file__).read_bytes(), 0o700)
run('systemd-run', '--unit=checkpoint-security-rollback', '--on-active=120s', '/usr/bin/python3', str(script), 'rollback')
try:
    atomic(ssh, b'# Managed by Checkpoint deployment\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nPermitRootLogin prohibit-password\n', 0o644)
    run('sshd', '-t')
    if not identical:
        # Add and persist narrow rules before removing the bootstrap-wide SSH rule.
        for network in networks:
            run('ufw', 'allow', 'proto', 'tcp', 'from', str(network), 'to', 'any',
                'port', str(port), 'comment', 'Checkpoint SSH')
        run('ufw', '--force', 'delete', 'allow', str(port) + '/tcp')
    run('systemctl', 'reload', 'ssh')
    # Refuse overrides in existing sshd configuration/Match blocks.
    effective = run('sshd', '-T', '-C', f'user=root,addr={peer},host=checkpoint-operator')
    require('passwordauthentication no\n' in effective and 'kbdinteractiveauthentication no\n' in effective, 'Existing SSH rules override key-only access')
    require('permitrootlogin prohibit-password\n' in effective or 'permitrootlogin without-password\n' in effective, 'Existing SSH rules override root policy')
    for name in UFW_FILES:
        setup['files'][name] = hashlib.sha256(Path(name).read_bytes()).hexdigest()
    atomic(SETUP_STATE, json.dumps(setup, indent=2).encode())
    atomic(metadata, json.dumps(policy).encode())
    print('Security policy staged; a second independent SSH connection must commit it within 120 seconds.')
except BaseException:
    rollback()
    raise
