#!/usr/bin/env bash
# Isolated native-process test; never reconfigures the running tailnet stack.
set -euo pipefail
cd "$(dirname "$0")/.."
[[ -f .dev/native/checkpoint-linux-amd64.tar.gz ]] || { echo 'Run scripts/build-native.sh first'; exit 1; }
[[ -f .dev/native/livekit.tar.gz ]] || curl -fsSL \
  https://github.com/livekit/livekit/releases/download/v1.13.11/livekit_1.13.11_linux_amd64.tar.gz \
  -o .dev/native/livekit.tar.gz
docker build -f deploy/native-test/Dockerfile -t checkpoint-native-test .
name="checkpoint-native-test-$$"
trap 'docker logs "$name" > .dev/native/runtime.log 2>&1; docker rm -f "$name" >/dev/null' EXIT
docker run -d --name "$name" --cpus=2 --memory=2g --cap-add NET_ADMIN -e "TEST_UID=$(id -u)" -e "TEST_GID=$(id -g)" \
  -v "$PWD/.dev/native:/work/.dev/native" checkpoint-native-test >/dev/null
for attempt in $(seq 1 30); do
  if docker exec "$name" python3 -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:18180/healthz")' >/dev/null 2>&1; then break; fi
  sleep 1
done
if [[ "${CHECKPOINT_NATIVE_GO:-1}" == 1 ]]; then
  docker run --rm --network "container:$name" --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -e GOCACHE=/work/.dev/go-build -e GOPATH=/work/.dev/go \
    -v "$PWD:/work" -w /work golang:1.27.3-bookworm \
    go test -tags=nativeintegration -count=1 -timeout=120s -v ./tests/native
fi
address=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name")
docker run --rm --ipc=private --shm-size=512m --user "$(id -u):$(id -g)" -e HOME=/tmp \
  --add-host "meet.checkpoint.test:$address" --add-host "rtc.checkpoint.test:$address" \
  --add-host "status.checkpoint.test:$address" --add-host "turn.checkpoint.test:$address" \
  -e CHECKPOINT_NATIVE_TEST=1 -e CHECKPOINT_NATIVE_INSECURE=1 \
  -v "$PWD:/work" -w /work/web \
  mcr.microsoft.com/playwright:v1.65.0-noble@sha256:6e3fb709361521eb27161606e6da6d2674e5e89fa16516cb37d6fdd91372273d \
  node node_modules/@playwright/test/cli.js test native.spec.ts foundation.spec.ts responsive.spec.ts call-layout.spec.ts device-check.spec.ts workspace-layout.spec.ts panels.spec.ts room-access.spec.ts

if [[ "${CHECKPOINT_LOAD:-0}" == 1 ]]; then
  docker run --rm --cpus=2 --memory=2g --user "$(id -u):$(id -g)" -e HOME=/tmp \
    --add-host "meet.checkpoint.test:$address" --add-host "rtc.checkpoint.test:$address" \
    --add-host "turn.checkpoint.test:$address" \
    -e SSL_CERT_FILE=/work/.dev/native/test-ca.pem -e GOCACHE=/work/.dev/go-build -e GOPATH=/work/.dev/go \
    -v "$PWD:/work" -w /work golang:1.27.3-bookworm \
    go run ./cmd/loadtest --seconds "${CHECKPOINT_LOAD_SECONDS:-30}" --joins 200
fi
