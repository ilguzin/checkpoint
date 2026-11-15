#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
docker build -q -f deploy/installer-test/Dockerfile -t checkpoint-installer-test .
docker run --rm --network none --user "$(id -u):$(id -g)" -e HOME=/tmp -e PYTHONDONTWRITEBYTECODE=1 \
  -v "$PWD:/work:ro" checkpoint-installer-test
