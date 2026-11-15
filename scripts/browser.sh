#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose --env-file "$PWD/.dev/compose.env" -f "$PWD/deploy/dev/compose.yaml")
if [[ "${CHECKPOINT_BROWSER_LOCAL:-0}" == 1 ]]; then
  # This recreates core and signal: use only while nobody is testing the stand.
  trap '"${compose[@]}" up -d --no-build --no-deps core signal' EXIT
  "${compose[@]}" -f deploy/dev/compose.browser-local.yaml up -d --no-build --no-deps core signal
fi
docker run --rm --network host --ipc=private --shm-size=512m \
  --user "$(id -u):$(id -g)" \
  -e CHECKPOINT_BROWSER_LOCAL="${CHECKPOINT_BROWSER_LOCAL:-0}" \
  -v "$PWD:/work" -w /work/web mcr.microsoft.com/playwright:v1.65.0-noble@sha256:6e3fb709361521eb27161606e6da6d2674e5e89fa16516cb37d6fdd91372273d \
  node node_modules/@playwright/test/cli.js test
