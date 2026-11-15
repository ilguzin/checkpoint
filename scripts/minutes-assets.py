#!/usr/bin/env python3
"""Install pinned local models and runtime. No implicit downloads during meetings."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request


def download(item, destination):
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists() and hashlib.file_digest(destination.open('rb'), 'sha256').hexdigest() == item['sha256']:
        return
    temporary = destination.with_suffix(destination.suffix + '.pending')
    try:
        with urllib.request.urlopen(item['url'], timeout=180) as source, temporary.open('wb') as output:
            shutil.copyfileobj(source, output, 1024 * 1024)
            output.flush()
            os.fsync(output.fileno())
        with temporary.open('rb') as source:
            if hashlib.file_digest(source, 'sha256').hexdigest() != item['sha256']:
                raise RuntimeError('SHA-256 mismatch: ' + destination.name)
        temporary.replace(destination)
    finally:
        temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--prefix', type=Path, default=Path('/opt/checkpoint-minutes'))
    parser.add_argument('--lock', type=Path, default=Path(__file__).resolve().parent.parent / 'deploy/minutes/models.lock.json')
    parser.add_argument('--models-only', action='store_true')
    args = parser.parse_args()
    lock = json.loads(args.lock.read_text())
    for model in lock['models']:
        directory = args.prefix / 'models' / ('whisper' if 'whisper' in model['id'] else 'generator')
        for item in model['files']:
            print('Verify/download', model['id'], item['name'], flush=True)
            download(item, directory / item['name'])
    if args.models_only:
        return
    source_archive = args.prefix / 'cache/llama.tar.gz'
    download(lock['llama'], source_archive)
    source_dir = args.prefix / ('llama-' + lock['llama']['commit'])
    if not source_dir.exists():
        with tempfile.TemporaryDirectory(dir=args.prefix) as temporary:
            with tarfile.open(source_archive, 'r:gz') as archive:
                archive.extractall(temporary, filter='data')
            extracted = list(Path(temporary).iterdir())
            if len(extracted) != 1 or not extracted[0].is_dir():
                raise RuntimeError('Unexpected llama source archive')
            extracted[0].rename(source_dir)
    # An earlier upstream build may have cached unpinned prebuilt UI assets.
    # Clear only our generated UI cache before building the API-only runtime.
    ui_cache = source_dir / 'build/tools/ui'
    for name in ('dist', 'ui-gzip'):
        shutil.rmtree(ui_cache / name, ignore_errors=True)
    for name in ('.ui-stamp', '.ui-embed.sha256'):
        (ui_cache / name).unlink(missing_ok=True)
    subprocess.run(['cmake', '-S', str(source_dir), '-B', str(source_dir / 'build'),
                    # Built on the destination VPS: detect its actual CPU.
                    # NATIVE=OFF enables AVX2/FMA by default upstream and crashes
                    # on older virtual CPUs such as Ivy Bridge.
                    '-DCMAKE_BUILD_TYPE=Release', '-DGGML_NATIVE=ON', '-DLLAMA_CURL=OFF',
                    '-DLLAMA_BUILD_TESTS=OFF', '-DLLAMA_BUILD_UI=OFF', '-DLLAMA_USE_PREBUILT_UI=OFF'], check=True)
    subprocess.run(['cmake', '--build', str(source_dir / 'build'), '-j2', '--target', 'llama-server'], check=True)
    runtime = args.prefix / 'llama'
    if runtime.is_symlink():
        runtime.unlink()
    runtime.symlink_to(source_dir / 'build/bin', target_is_directory=True)
    (args.prefix / 'installed-lock.json').write_text(json.dumps(lock, indent=2) + '\n')


if __name__ == '__main__':
    main()
