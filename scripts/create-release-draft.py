#!/usr/bin/env python3
"""Create/resume an unsigned CI draft without replacing existing assets."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys

repo = 'ilguzin/checkpoint'
tag = sys.argv[1]
if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', tag):
    raise SystemExit('Invalid stable version')
run = subprocess.run
listing = json.loads(subprocess.check_output(['gh', 'api', '--paginate', '--slurp', f'repos/{repo}/releases?per_page=100']))
existing = next((r for page in listing for r in page if r['tag_name'] == tag), None)
if existing is None:
    run(['gh', 'release', 'create', tag, '--repo', repo, '--verify-tag', '--draft', '--title', f'Checkpoint {tag}', '--notes', 'Unsigned draft. The owner must verify and sign release.json locally before publication.'], check=True)
    existing = json.loads(subprocess.check_output(['gh','api',f'repos/{repo}/releases/tags/{tag}']))
if not existing['draft'] or existing.get('immutable'):
    raise SystemExit('Refusing to modify a published/immutable release')
assets = {a['name']: a for a in existing['assets']}
if any(name in assets for name in ('release.json','release.json.minisig')):
    raise SystemExit('Draft is already signed; resume through the local release tool')
for name in ('checkpoint-linux-amd64.tar.gz', 'install.sh'):
    path = Path('.dev/native') / name
    if name in assets:
        item = assets[name]
        content = subprocess.check_output(['gh','api',f"repos/{repo}/releases/assets/{item['id']}",'-H','Accept: application/octet-stream'])
        if content != path.read_bytes():
            raise SystemExit('Existing draft asset differs: ' + name)
    else:
        run(['gh','release','upload',tag,str(path),'--repo',repo],check=True)
print('Unsigned draft ready. No private signing key is used in CI.')
