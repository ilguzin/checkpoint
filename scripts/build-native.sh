#!/usr/bin/env bash
# Build on the workstation; no Docker, Node or Go is required on the VPS.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .dev/native
revision=$(git rev-parse HEAD)
version="${CHECKPOINT_BUILD_VERSION:-dev-$(git rev-parse --short HEAD)}"
if [[ -n "$(git status --porcelain)" ]]; then
  [[ -z "${CHECKPOINT_BUILD_VERSION:-}" ]] || { echo 'Release builds require a clean checkout'; exit 1; }
  version+="-dirty"
fi
key="${CHECKPOINT_RELEASE_PUBLIC_KEY:-}"
[[ -z "$key" || "$key" =~ ^[A-Za-z0-9+/=]{56}$ ]] || { echo 'Invalid Minisign public key'; exit 1; }
[[ -z "${CHECKPOINT_BUILD_VERSION:-}" || -n "$key" ]] || { echo 'Release builds require a pinned public key'; exit 1; }
image="${CHECKPOINT_BUILD_IMAGE:-checkpoint-native-build}"
docker build --platform linux/amd64 --target go-build \
  --build-arg "CHECKPOINT_VERSION=$version" --build-arg "CHECKPOINT_RELEASE_PUBLIC_KEY=$key" -t "$image" .
container=$(docker create "$image" /checkpoint)
trap 'docker rm "$container" >/dev/null' EXIT
docker cp "$container:/checkpoint" .dev/native/checkpoint
python3 - "$revision" "$version" <<'PY'
import gzip, hashlib, io, json, pathlib, sys, tarfile
root = pathlib.Path('.dev/native')
files = {'checkpoint': (root / 'checkpoint').read_bytes(), 'LICENSE': pathlib.Path('LICENSE').read_bytes(), 'setup.sh': pathlib.Path('scripts/setup.sh').read_bytes(), 'third_party/catalog.json': pathlib.Path('internal/notices/catalog.json').read_bytes()}
for name in ['scripts/transcribe.py', 'scripts/minutes-assets.py', 'scripts/minutes-setup.py', 'deploy/minutes/models.lock.json', 'deploy/minutes/requirements.lock']:
    files[name] = pathlib.Path(name).read_bytes()
for path in sorted(pathlib.Path('third_party').rglob('*')):
    if path.is_file():
        files[str(path)] = path.read_bytes()
manifest = dict(format=1, platform='linux-amd64', commit=sys.argv[1], version=sys.argv[2],
                files={name: hashlib.sha256(data).hexdigest() for name, data in files.items()})
files['manifest.json'] = (json.dumps(manifest, indent=2) + '\n').encode()
output = root / 'checkpoint-linux-amd64.tar.gz'
with output.open('wb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', filename='', mtime=0) as zipped, tarfile.open(fileobj=zipped, mode='w') as archive:
    for name, data in files.items():
        info = tarfile.TarInfo(name)
        info.size, info.mode = len(data), 0o755 if name == 'checkpoint' else 0o644
        archive.addfile(info, io.BytesIO(data))
print(f'{output}  sha256:{hashlib.sha256(output.read_bytes()).hexdigest()}')
print(f'Prototype bundle: {sys.argv[2]} ({sys.argv[1]}). Not a signed release.')
PY
