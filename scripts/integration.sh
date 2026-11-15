#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# Use a builder container, the host network, and the current user's Docker socket
# permissions. Restart checks are opt-in because they interrupt the test stand.
docker_gid="$(stat -c '%g' /var/run/docker.sock)"
docker run --rm --network host --user "$(id -u):$(id -g)" --group-add "$docker_gid" \
  -e GOCACHE=/work/.dev/go-build -e GOPATH=/work/.dev/go \
  -e CHECKPOINT_TEST_RESTARTS="${CHECKPOINT_TEST_RESTARTS:-0}" \
  -v "$PWD:/work" -v /var/run/docker.sock:/var/run/docker.sock \
  -v /usr/bin/docker:/usr/bin/docker:ro \
  -v /usr/libexec/docker/cli-plugins:/usr/libexec/docker/cli-plugins:ro \
  -w /work golang:1.27.3-bookworm \
  go test -tags=integration -count=1 -timeout=180s -v ./tests/integration
