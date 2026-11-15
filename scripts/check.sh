#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 scripts/public-content-check.py
mkdir -p .dev/go-build .dev/go
npm --prefix web ci --ignore-scripts
npm --prefix web run build
docker run --rm --user "$(id -u):$(id -g)" \
  -e GOCACHE=/work/.dev/go-build -e GOPATH=/work/.dev/go \
  -v "$PWD:/work" -w /work golang:1.27.3-bookworm \
  sh -c 'go test -race ./... && go vet ./... && go build -o /work/.dev/checkpoint ./cmd/checkpoint'
python3 -m unittest discover -s scripts -p '*_test.py'
git diff --check
