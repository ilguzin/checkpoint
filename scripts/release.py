#!/usr/bin/env python3
"""Local release signing. The encrypted owner key never leaves this workstation."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

REPOSITORY = 'ilguzin/checkpoint'
VERSION = re.compile(r'v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\Z')
MAX_ASSET = 300 * 1024 * 1024
SCHEMA_SOURCE = Path(__file__).resolve().parents[1] / 'internal/core/store.go'


def require(ok, message):
    if not ok:
        raise ValueError(message)


def source_schema():
    """Use the same schema as the binary built from this release checkout."""
    matches = re.findall(r'^const\s+SchemaVersion\s*=\s*([1-9][0-9]*)\s*$', SCHEMA_SOURCE.read_text(), re.MULTILINE)
    require(len(matches) == 1, 'Cannot determine SchemaVersion from this checkout; inspect the source or pass --schema explicitly')
    return int(matches[0])


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def key_paths():
    secret = Path(os.environ.get('CHECKPOINT_SIGNING_KEY_FILE', '~/.config/checkpoint/release.key')).expanduser().absolute()
    public = secret.with_suffix('.pub')
    require(secret != public, 'Private key filename must not end in .pub')
    return secret, public


def keygen(force=False):
    require(shutil.which('minisign'), 'Install minisign locally before generating or signing releases')
    secret, public = key_paths()
    require(not secret.is_symlink() and not public.is_symlink(), 'Key paths must not be symlinks')
    if secret.exists() and not force:
        require(secret.is_file() and secret.stat().st_mode & 0o077 == 0, 'Private key must have mode 0600')
        if not public.exists():
            run('minisign', '-R', '-s', str(secret), '-p', str(public))
        print('Existing signing key retained.')
        return secret, public
    require(not public.exists() or force, 'Only public key exists; recover the private key or explicitly use keygen --force')
    secret.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    run('minisign', '-G', *(['-f'] if force else []), '-s', str(secret), '-p', str(public))
    secret.chmod(0o600)
    print('Encrypted local signing key created. Back it up securely; --force does not rotate installed trust.')
    return secret, public


def release_metadata(tag, repo):
    # GET /releases/tags/{tag} returns published releases only; drafts appear in the authenticated list.
    # Page manually: older gh (Ubuntu 24.04 ships 2.45) has no `gh api --slurp`.
    found, page = [], 1
    while True:
        result = run('gh', 'api', f'repos/{repo}/releases?per_page=100&page={page}', capture_output=True, text=True)
        batch = json.loads(result.stdout)
        found += [value for value in batch if value.get('tag_name') == tag]
        if len(batch) < 100:
            break
        page += 1
    require(found, f'No release or draft for {tag}; wait for the release workflow')
    require(len(found) == 1, f'Several releases use tag {tag}; inspect them on GitHub')
    return found[0]


def fingerprint(assets):
    return sorted((a['id'], a['name'], a['size'], a.get('digest'), a['updated_at']) for a in assets)


def asset_kind(name):
    require(re.fullmatch(r'[A-Za-z0-9._-]{1,120}', name), 'Unsafe asset name')
    if name == 'checkpoint-linux-amd64.tar.gz':
        return 'checkpoint'
    if name.startswith('livekit') and name.endswith('_linux_amd64.tar.gz'):
        return 'livekit'
    if name == 'install.sh':
        return 'installer'
    raise ValueError(f'Unsupported release asset: {name}')


def download_asset(repo, asset, output):
    require(0 < asset['size'] <= MAX_ASSET, 'Asset size is outside limits')
    with output.open('xb') as target:
        proc = subprocess.Popen(['gh', 'api', f"repos/{repo}/releases/assets/{asset['id']}", '-H', 'Accept: application/octet-stream'], stdout=subprocess.PIPE)
        try:
            total = 0
            while chunk := proc.stdout.read(1 << 20):
                total += len(chunk)
                require(total <= asset['size'], 'Asset grew during download')
                target.write(chunk)
            require(proc.wait() == 0 and total == asset['size'], 'Asset download failed or size changed')
        finally:
            if proc.poll() is None:
                proc.kill()
            proc.wait()


def manifest_for(tag, sequence, livekit, schema, assets):
    require(VERSION.fullmatch(tag), 'Stable semantic version required, e.g. v0.1.0')
    require(sequence > 0 and schema > 0, 'Positive release sequence and schema required')
    require(re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', livekit), 'Invalid LiveKit version')
    require(sum(a['kind'] == 'checkpoint' for a in assets) == 1, 'Exactly one Checkpoint amd64 bundle required')
    require(len({a['name'] for a in assets}) == len(assets), 'Duplicate asset name')
    return dict(format=1, project='checkpoint', repository=REPOSITORY, channel='stable', sequence=sequence,
                version=tag, livekitVersion=livekit, schema=dict(min=schema, max=schema, target=schema), assets=assets)


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'Duplicate JSON key')
        result[key] = value
    return result


def verify_release_directory(directory, public, tag, sequence=None):
    raw = (directory / 'release.json').read_bytes()
    run('minisign', '-V', '-p', str(public), '-m', str(directory / 'release.json'), '-x', str(directory / 'release.json.minisig'))
    manifest = json.loads(raw, object_pairs_hook=unique)
    require(manifest.get('project') == 'checkpoint' and manifest.get('repository') == REPOSITORY
            and manifest.get('format') == 1 and manifest.get('channel') == 'stable' and manifest.get('version') == tag, 'Wrong signed release identity')
    if sequence is not None:
        require(manifest.get('sequence') == sequence, 'Wrong release sequence')
    require(type(manifest.get('sequence')) is int and manifest['sequence'] > 0, 'Invalid release sequence')
    entries = manifest.get('assets', [])
    require(isinstance(entries, list) and 1 <= len(entries) <= 8, 'Invalid release asset list')
    require(len({e['name'] for e in entries}) == len(entries), 'Duplicate signed release assets')
    require({'checkpoint-linux-amd64.tar.gz', 'install.sh'} <= {e['name'] for e in entries}, 'Signed bundle and installer required')
    for entry in entries:
        require(entry.get('kind') == asset_kind(entry['name']) and entry.get('os') == 'linux' and entry.get('arch') == 'amd64', 'Signed asset platform/kind mismatch')
        require(type(entry.get('size')) is int and 0 < entry['size'] <= MAX_ASSET, 'Signed asset size outside limits')
        path = directory / entry['name']
        require(path.is_file() and not path.is_symlink() and path.stat().st_size == entry['size'], 'Signed artifact size mismatch')
        require(hashlib.sha256(path.read_bytes()).hexdigest() == entry['sha256'], 'Signed artifact hash mismatch')
    return manifest


def sign(args):
    require(args.repo == REPOSITORY, 'Release repository must be ilguzin/checkpoint')
    require(VERSION.fullmatch(args.tag), 'Invalid stable release tag')
    secret, public = keygen()
    before = release_metadata(args.tag, args.repo)
    require(not before.get('prerelease'), 'Stable signing rejects prereleases')
    assets = [a for a in before['assets'] if a['name'] not in ('release.json', 'release.json.minisig')]
    require(len({a['name'] for a in before['assets']}) == len(before['assets']), 'Duplicate GitHub asset name')
    signed_assets = {a['name']: a for a in before['assets'] if a['name'] in ('release.json', 'release.json.minisig')}
    require(1 <= len(assets) <= 8, 'Unexpected asset count')
    require({'checkpoint-linux-amd64.tar.gz', 'install.sh'} <= {a['name'] for a in assets}, 'Bundle and installer are required')
    require('release.json.minisig' not in signed_assets or 'release.json' in signed_assets, 'Orphan signature; inspect the draft')
    complete = len(signed_assets) == 2
    require(complete or before.get('draft') and not before.get('immutable'), 'Signatures can only be added to a mutable draft')
    with tempfile.TemporaryDirectory(prefix='checkpoint-sign-') as temporary:
        directory = Path(temporary)
        entries = []
        for asset in assets:
            kind = asset_kind(asset['name'])
            path = directory / asset['name']
            download_asset(args.repo, asset, path)
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            if asset.get('digest'):
                require(asset['digest'] == 'sha256:' + digest, 'GitHub asset digest mismatch')
            entries.append(dict(name=asset['name'], os='linux', arch='amd64', size=asset['size'], sha256=digest, kind=kind))
        entries.sort(key=lambda value: value['name'])
        manifest = manifest_for(args.tag, args.sequence, args.livekit_version, args.schema, entries)
        key = public.read_text().strip().splitlines()[-1]
        installer = (directory / 'install.sh').read_text()
        expected = Path(__file__).with_name('install.sh').read_text().replace('__CHECKPOINT_PUBLIC_KEY__', key).replace('__CHECKPOINT_SCHEMA__', str(args.schema))
        require(installer == expected, 'Draft installer differs from this checkout, public key or schema')
        path, signature = directory / 'release.json', directory / 'release.json.minisig'
        for name, asset in signed_assets.items():
            download_asset(args.repo, asset, directory / name)
        if path.exists():
            existing = json.loads(path.read_bytes(), object_pairs_hook=unique)
            # Preserve exact previously uploaded bytes, including original ordering.
            existing_entries = existing.get('assets', [])
            require(isinstance(existing_entries, list), 'Invalid existing manifest')
            existing['assets'] = sorted(existing_entries, key=lambda value: value['name'])
            require(existing == manifest, 'Existing manifest differs from release artifacts; refusing to replace it')
        else:
            path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + '\n')
        if not signature.exists():
            run('minisign', '-S', '-s', str(secret), '-m', str(path), '-x', str(signature), '-t', f'checkpoint {args.tag} sequence {args.sequence}')
        verify_release_directory(directory, public, args.tag, args.sequence)
        after = release_metadata(args.tag, args.repo)
        require(before['id'] == after['id'] and fingerprint(before['assets']) == fingerprint(after['assets']), 'Release changed while signing; nothing uploaded')
        missing = [str(directory / name) for name in ('release.json', 'release.json.minisig') if name not in signed_assets]
        if missing:
            run('gh', 'release', 'upload', args.tag, *missing, '--repo', args.repo)
        uploaded = release_metadata(args.tag, args.repo)
        original = [a for a in uploaded['assets'] if a['name'] not in ('release.json', 'release.json.minisig')]
        require(fingerprint(original) == fingerprint(assets), 'Release assets changed during upload; inspect the draft')
    print('Release signature verified. Missing signatures uploaded; publication status unchanged.')


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    generate = commands.add_parser('keygen')
    generate.add_argument('--force', action='store_true')
    signing = commands.add_parser('sign')
    signing.add_argument('tag')
    signing.add_argument('--repo', default=REPOSITORY)
    signing.add_argument('--sequence', required=True, type=int)
    signing.add_argument('--schema', type=int, help='database schema (default: SchemaVersion from this checkout)')
    signing.add_argument('--livekit-version', default='1.13.11')
    installer = commands.add_parser('installer')
    installer.add_argument('--public-key', required=True, type=Path)
    installer.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    if args.command == 'installer':
        lines = args.public_key.read_text().strip().splitlines()
        key = lines[-1]
        require(re.fullmatch(r'[A-Za-z0-9+/=]{56}', key), 'Malformed Minisign public key')
        template = Path(__file__).with_name('install.sh').read_text()
        rendered = template.replace('__CHECKPOINT_PUBLIC_KEY__', key).replace('__CHECKPOINT_SCHEMA__', str(source_schema()))
        if args.output.exists():
            require(not args.output.is_symlink() and args.output.read_text() == rendered, 'Existing installer differs; use a new output path')
        else:
            with args.output.open('x') as output:
                output.write(rendered)
        args.output.chmod(0o755)
        print('Installer rendered with pinned public key.')
    elif args.command == 'keygen':
        keygen(args.force)
    else:
        if args.schema is None:
            args.schema = source_schema()
        sign(args)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error))
