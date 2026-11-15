#!/usr/bin/env bash
set -euo pipefail
exec python3 - "$@" <<'CHECKPOINT_INSTALL_PY'
import argparse, hashlib, io, ipaddress, json, os, platform, re, shlex, shutil, subprocess, sys, tarfile, tempfile, urllib.request
from pathlib import Path

# Render with scripts/release.py installer before publishing the bootstrap script.
PUBLIC_KEY = '__CHECKPOINT_PUBLIC_KEY__'
SCHEMA = '__CHECKPOINT_SCHEMA__'
REPO = 'ilguzin/checkpoint'
LIMIT = 300 * 1024 * 1024

def require(ok, message):
    if not ok:
        raise SystemExit(message)

def compatible_schema(schema):
    return (isinstance(schema, dict) and set(schema) == {'min', 'max', 'target'}
            and all(type(value) is int and value > 0 for value in schema.values())
            and schema['min'] <= int(SCHEMA) <= schema['max']
            and schema['target'] == int(SCHEMA))

def fetch(url, limit):
    with urllib.request.urlopen(url, timeout=60) as response:
        data = response.read(limit + 1)
    require(len(data) <= limit, 'Download exceeds size limit')
    return data

def unique(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value, 'Duplicate JSON key')
        value[key] = item
    return value

def parse(data):
    return json.loads(data, object_pairs_hook=unique)

def arguments():
    parser = argparse.ArgumentParser(prog='checkpoint-install.sh', description='Install Checkpoint on a clean Ubuntu 24.04 amd64 VPS. No arguments starts an interactive wizard. Downloads and signatures are verified before setup; --help is offline.')
    parser.add_argument('--verify-only', action='store_true', help='verify the signed bundle and setup interface without installing')
    parser.add_argument('--release-dir', type=Path, help='verify a locally downloaded signed draft (requires --verify-only)')
    parser.add_argument('--version', help='fixed stable release tag; default: latest public stable release')
    parser.add_argument('--public-ip')
    for name in ('app', 'rtc', 'turn', 'observer'):
        parser.add_argument('--' + name + '-domain')
    contact = parser.add_mutually_exclusive_group()
    contact.add_argument('--email')
    contact.add_argument('--no-email', action='store_true')
    parser.add_argument('--ssh-port', type=int, default=22)
    parser.add_argument('--livekit-archive')
    parser.add_argument('--apply', action='store_true', help='install packages and services, configure UFW and accept ACME terms')
    result = parser.parse_args()
    require(not result.release_dir or result.verify_only, '--release-dir is allowed only with --verify-only')
    require(not result.verify_only or not result.apply, '--verify-only cannot be combined with --apply')
    return result


def wizard(options):
    try:
        terminal = open('/dev/tty', 'r+')
    except OSError:
        raise SystemExit('No terminal for the setup wizard. Pass explicit domain/IP flags; see --help.')
    with terminal:
        def ask(prompt, default=''):
            terminal.write(prompt + (f' [{default}]' if default else '') + ': ')
            terminal.flush()
            answer = terminal.readline()
            require(answer != '', 'Setup cancelled: terminal closed')
            return answer.strip() or default
        terminal.write('Checkpoint setup — clean Ubuntu 24.04 amd64, one VPS.\n')
        options.public_ip = ask('Public IPv4 of this VPS')
        address = ipaddress.ip_address(options.public_ip)
        require(address.version == 4 and address.is_global, 'A direct public IPv4 is required')
        base = ask('Base domain, or leave empty to use sslip.io', options.public_ip + '.sslip.io').lower()
        options.app_domain = ask('Application hostname', base if base.endswith('.sslip.io') else 'meet.' + base)
        options.rtc_domain = ask('Signalling hostname', 'rtc.' + base)
        options.turn_domain = ask('TURN hostname', 'turn.' + base)
        options.observer_domain = ask('Public status hostname', 'status.' + base)
        options.ssh_port = int(ask('SSH port to keep open', '22'))
        options.email = ask('ACME email (optional; empty means no email)') or None
        options.no_email = not options.email
        terminal.write('DNS must point to this VPS; no AAAA or HTTP proxy. Check the provider firewall.\n')
        terminal.write('Without --apply the installer only verifies the release and runs preflight.\n')
        options.apply = ask('Install and accept ACME terms now? Type yes; default is preflight', 'no').lower() == 'yes'


def setup_arguments(options):
    require(options.public_ip and all(getattr(options, n + '_domain') for n in ('app','rtc','turn','observer')), 'IP and all four domain names are required; run without arguments for the wizard')
    address = ipaddress.ip_address(options.public_ip)
    require(address.version == 4 and address.is_global, 'A direct public IPv4 is required')
    require(bool(options.email) != bool(options.no_email), 'Choose --email ADDRESS or --no-email')
    require(1 <= options.ssh_port <= 65535, 'Invalid SSH port')
    result = ['--public-ip', options.public_ip, '--ssh-port', str(options.ssh_port)]
    domains = []
    for name in ('app','rtc','turn','observer'):
        domain = getattr(options, name + '_domain')
        require(len(domain) <= 253 and '.' in domain and all(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label) for label in domain.split('.')), 'Use lowercase ASCII hostnames')
        domains.append(domain)
        result += ['--' + name + '-domain', domain]
    require(len(set(domains)) == 4, 'Four distinct domain names are required')
    result += ['--email', options.email] if options.email else ['--no-email']
    if options.livekit_archive:
        result += ['--livekit-archive', options.livekit_archive]
    if options.apply:
        result.append('--apply')
    return result


os.umask(0o077)
options = arguments()
require(re.fullmatch(r'[A-Za-z0-9+/=]{56}', PUBLIC_KEY), 'Installer has no pinned owner key; publish a rendered installer first')
require(platform.system() == 'Linux' and platform.machine() == 'x86_64', 'Only Linux amd64 is supported')
if not sys.argv[1:]:
    wizard(options)
args = [] if options.verify_only else setup_arguments(options)
apply = options.apply
if not options.verify_only:
    print('Setup arguments: ' + shlex.join(args))
if not shutil.which('minisign'):
    require(apply and os.geteuid() == 0, 'Install minisign from your OS repository, or use --apply as root')
    release = dict(line.split('=', 1) for line in Path('/etc/os-release').read_text().splitlines() if '=' in line)
    require(release.get('ID', '').strip('"') == 'ubuntu' and release.get('VERSION_ID', '').strip('"') == '24.04', 'Ubuntu 24.04 required')
    subprocess.run(['apt-get', 'update'], check=True)
    subprocess.run(['apt-get', 'install', '-y', '--no-install-recommends', 'minisign', 'ca-certificates'], check=True)
if options.release_dir:
    require(options.version, '--release-dir requires --version to verify the expected release identity')
    tag = options.version
elif options.version:
    tag = options.version
else:
    metadata = parse(fetch('https://api.github.com/repos/' + REPO + '/releases/latest', 1 << 20))
    tag = metadata.get('tag_name', '')
    require(not metadata.get('draft') and not metadata.get('prerelease'), 'Stable release required')
require(re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', tag), 'Stable release required')
base = 'https://github.com/' + REPO + '/releases/download/' + tag

def artifact(name, limit):
    if options.release_dir:
        path = options.release_dir / name
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= limit, 'Invalid local release artifact')
        data = path.read_bytes()
        require(len(data) <= limit, 'Local artifact exceeds size limit')
        return data
    return fetch(base + '/' + name, limit)

with tempfile.TemporaryDirectory(prefix='checkpoint-install-') as directory:
    directory = Path(directory)
    body = artifact('release.json', 1 << 20)
    (directory / 'release.json').write_bytes(body)
    (directory / 'release.json.minisig').write_bytes(artifact('release.json.minisig', 8192))
    subprocess.run(['minisign', '-V', '-P', PUBLIC_KEY, '-m', str(directory / 'release.json')], check=True)
    manifest = parse(body)
    require(set(manifest) == {'format','project','repository','channel','sequence','version','livekitVersion','schema','assets'}, 'Unsupported manifest fields')
    require(manifest['format'] == 1 and manifest['project'] == 'checkpoint' and manifest['repository'] == REPO and manifest['channel'] == 'stable' and manifest['version'] == tag and type(manifest['sequence']) is int and manifest['sequence'] > 0, 'Release identity mismatch')
    require(manifest['livekitVersion'] == '1.13.11' and compatible_schema(manifest['schema']), 'Unsupported schema/media version')
    assets = manifest['assets']
    require(isinstance(assets, list) and 1 <= len(assets) <= 8, 'Invalid assets')
    names = set()
    for asset in assets:
        require(set(asset) == {'name','os','arch','size','sha256','kind'}, 'Unsupported asset fields')
        require(re.fullmatch(r'[A-Za-z0-9._-]{1,120}', asset['name']) and asset['name'] not in ('.', '..') and asset['name'] not in names, 'Unsafe or duplicate asset name')
        require(asset['kind'] in ('checkpoint', 'livekit', 'installer'), 'Unknown asset kind')
        require(asset['os'] == 'linux' and asset['arch'] == 'amd64' and type(asset['size']) is int and 0 < asset['size'] <= LIMIT and re.fullmatch(r'[a-f0-9]{64}', asset['sha256']), 'Unsupported asset')
        names.add(asset['name'])
    candidates = [a for a in assets if a['kind'] == 'checkpoint']
    require(len(candidates) == 1 and candidates[0]['name'] == 'checkpoint-linux-amd64.tar.gz', 'Checkpoint bundle missing')
    asset = candidates[0]
    archive = artifact(asset['name'], asset['size'])
    require(len(archive) == asset['size'] and hashlib.sha256(archive).hexdigest() == asset['sha256'], 'Bundle hash mismatch')
    bundle = directory / asset['name']
    bundle.write_bytes(archive)
    members = {}
    total = 0
    with tarfile.open(fileobj=io.BytesIO(archive), mode='r:gz') as tar:
        for entry in tar:
            require(entry.isfile() and not entry.name.startswith('/') and all(p not in ('', '.', '..') for p in entry.name.split('/')) and entry.name not in members, 'Unsafe bundle archive')
            total += entry.size
            require(total <= LIMIT, 'Expanded bundle exceeds size limit')
            members[entry.name] = tar.extractfile(entry).read()
    require('setup.sh' in members and 'checkpoint' in members, 'Installer/binary missing from signed bundle')
    require('manifest.json' in members, 'Internal bundle manifest missing')
    internal = parse(members.pop('manifest.json'))
    require(internal.get('format') == 1 and internal.get('version') == tag and internal.get('platform') == 'linux-amd64', 'Internal bundle identity mismatch')
    require(internal.get('files') == {name: hashlib.sha256(data).hexdigest() for name, data in members.items()}, 'Internal bundle hashes mismatch')
    setup = directory / 'setup.sh'; setup.write_bytes(members['setup.sh']); setup.chmod(0o700)
    binary = directory / 'checkpoint'; binary.write_bytes(members['checkpoint']); binary.chmod(0o700)
    embedded = subprocess.run([str(binary), 'trust-key'], capture_output=True, text=True, check=True).stdout.strip()
    require(embedded == PUBLIC_KEY, 'Binary and installer trust keys differ')
    help_text = subprocess.run([str(setup), '--help'], check=True, capture_output=True, text=True).stdout
    for flag in ('--public-ip','--app-domain','--rtc-domain','--turn-domain','--observer-domain','--no-email','--email','--ssh-port','--bundle','--apply'):
        require(flag in help_text, 'Signed setup does not support ' + flag)
    if options.verify_only:
        print('Verified Checkpoint ' + tag + ': signature, bundle hashes, embedded trust key and setup interface. No installation performed.')
        raise SystemExit(0)
    state = dict(sequence=manifest['sequence'], digest=hashlib.sha256(body).hexdigest(), version=tag)
    state_path = Path('/etc/checkpoint/update-state.json')
    if state_path.exists():
        require(parse(state_path.read_bytes()) == state, 'Use checkpoint update for an existing different release')
    subprocess.run([str(setup), '--bundle', str(bundle), *args], check=True)
    if apply:
        for path, data in [(state_path, (json.dumps(state, indent=2) + '\n').encode()), (Path('/etc/checkpoint/release.pub'), (PUBLIC_KEY + '\n').encode())]:
            temporary = path.with_suffix(path.suffix + '.new')
            with temporary.open('xb') as output:
                output.write(data); output.flush(); os.fsync(output.fileno())
            os.replace(temporary, path)
        print('Signed installation completed. Run checkpoint users add locally to create the first organizer.')
CHECKPOINT_INSTALL_PY
