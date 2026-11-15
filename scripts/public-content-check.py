#!/usr/bin/env python3
"""Check publication files, not Git history or GitHub-hosted metadata."""
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import unquote

PRIVATE_PREFIXES = ('.dev/', '.artifacts/', '.ssh/', 'docs/stages/', 'docs/reports/',
                    'docs/adr/', 'docs/plans/', 'docs/reviews/', 'docs/archive/')
PRIVATE_FILES = {'DEVELOPMENT_PLAN.md', 'docs/product.md', 'docs/reliability.md', 'docs/sources.md'}
LOCAL_SUFFIXES = ('.db', '.sqlite', '.sqlite3', '.db-wal', '.db-shm', '.key', '.bundle', '.local.yaml')
INTERNAL_REFERENCE = re.compile(
    r'DEVELOPMENT_PLAN\.md|(?:docs/)?(?:stages|reports)/M\d|/home/[A-Za-z0-9_-]+/'
    r'|(?:\d{1,3}\.){3}\d{1,3}\.sslip\.io|tail[a-z0-9]{6}\.ts\.net')
SECRET_MARKERS = (
    re.compile(r'-----BEGIN (?:OPENSSH |RSA |EC |DSA |ENCRYPTED )?PRIVATE KEY-----'),
    re.compile(r'\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})\b'),
)
LINK = re.compile(r'!?\[[^\]]*\]\((?:<([^>]+)>|([^\s)]+))(?:\s+"[^"]*")?\)')


def audit(root, names):
    root = Path(root).resolve()
    errors = []
    for name in sorted(set(names)):
        if not name:
            continue
        path = root / name
        if (name in PRIVATE_FILES or name.startswith(PRIVATE_PREFIXES)
                or name.endswith(LOCAL_SUFFIXES) or path.name == '.env'
                or (path.name.startswith('.env.') and path.name != '.env.example')):
            errors.append(f'{name}: private planning or local runtime file in public tree')
        if not path.resolve().is_relative_to(root):
            errors.append(f'{name}: path or symlink escapes repository')
            continue
        if not path.is_file():
            continue
        data = path.read_bytes()
        if b'\0' in data:
            continue
        text = data.decode('utf-8', errors='replace')
        if any(marker.search(text) for marker in SECRET_MARKERS):
            # Report locations only; never copy credentials into CI logs.
            errors.append(f'{name}: private key or access-token marker')
        if path.suffix in ('.md', '.yaml', '.yml') and INTERNAL_REFERENCE.search(text):
            errors.append(f'{name}: internal reference')
        if path.suffix == '.md':
            for match in LINK.finditer(text):
                target = match.group(1) or match.group(2)
                if target.startswith(('#', 'http:', 'https:', 'mailto:')):
                    continue
                target = unquote(target.split('#')[0])
                resolved = (path.parent / target).resolve()
                if target and (not resolved.is_relative_to(root) or not resolved.exists()):
                    errors.append(f'{name}: broken or external local link {target}')
    return errors


def main():
    root = Path(__file__).resolve().parents[1]
    paths = subprocess.check_output(
        ['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=root
    ).decode().split('\0')
    errors = audit(root, paths)
    if errors:
        print('\n'.join(errors), file=sys.stderr)
        return 1
    print('Public snapshot: planning/runtime files, key/token markers and local links checked. '
          'This is not a complete secret scan. Git history and GitHub metadata are not checked.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
