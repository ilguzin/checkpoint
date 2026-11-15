#!/usr/bin/env python3
"""Collect pinned runtime dependency LICENSE/NOTICE files into the embedded catalog."""
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def collect():
    command = ['docker', 'run', '--rm', '--user', f'{__import__("os").getuid()}:{__import__("os").getgid()}',
               '-e', 'GOPATH=/work/.dev/go', '-e', 'GOCACHE=/work/.dev/go-build', '-v', str(ROOT) + ':/work', '-w', '/work',
               'golang:1.27.3-bookworm', 'go', 'list', '-deps', '-f', '{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}', './cmd/checkpoint']
    output = subprocess.run(command, check=True, capture_output=True, text=True).stdout
    entries = {('checkpoint', 'source'): ROOT}
    for line in output.splitlines():
        if not line or line.startswith('github.com/ilguzin/checkpoint|'):
            continue
        name, version, directory = line.split('|')
        entries[(name, version)] = Path(directory.replace('/work/', str(ROOT) + '/', 1))
    lock = json.loads((ROOT / 'web/package-lock.json').read_text())
    for name, value in lock['packages'].items():
        # Optional TypeScript/@types peers support compilation only; their platform
        # packages are not shipped in the browser bundle.
        if (not name or value.get('dev') or value.get('devOptional')
                or name.startswith('node_modules/@typescript/')
                or name.startswith('node_modules/@types/')):
            continue
        directory = ROOT / 'web' / name
        package = json.loads((directory / 'package.json').read_text())
        entries[(package['name'], package['version'])] = directory
    result = []
    missing = []
    for (name, version), directory in sorted(entries.items()):
        if name == '@bufbuild/protobuf' and version == '1.10.1':
            directory = ROOT / 'third_party/licenses/bufbuild-protobuf-1.10.1'
        files = [p for p in sorted(directory.iterdir()) if p.is_file() and p.name.upper().startswith(('LICENSE', 'COPYING', 'NOTICE', 'COPYRIGHT'))]
        if not files:
            missing.append(name)
            continue
        result.append(dict(name=name, version=version, licenses=[dict(file=p.name, text=p.read_text(errors='strict')) for p in files]))
    if missing:
        raise SystemExit('License files missing: ' + ', '.join(missing))
    target = ROOT / 'internal/notices/catalog.json'
    target.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(f'Collected {len(result)} dependency entries; review notices before distribution.')


if __name__ == '__main__':
    collect()
