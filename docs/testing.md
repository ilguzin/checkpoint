# Testing and load strategy

This document describes the system contract and operational limitations. Target metrics are not an SLA guarantee.

## Local environment

The prototype targets a private test tailnet. It requires Linux, Docker Engine + Compose, Python 3, an existing Tailscale installation with HTTPS for the node name, and the available ports listed below. Scripts do not register the machine, change ACLs/grants, or enable Funnel.

```bash
./scripts/dev.sh prepare
./scripts/dev.sh up
./scripts/dev.sh links
./scripts/dev.sh status
```

`prepare` creates separate random invitations for Meeting, Webinar host, and Webinar viewer, a LiveKit key, and configurations under `.dev/` with restricted permissions. Rerunning preserves them and role state. `links` prints **secret** invitation links: share them only with testers, never in public reports. Invitations are passed in a URL fragment and removed from the address bar after being read; manual entry is also available.

If Tailscale returns `serve config denied`, the machine administrator runs `sudo tailscale set --operator=YOUR_USER` once (substitute the appropriate username), or configures the three Serve routes manually. Then rerun `up`. A working Tailscale installation does not imply permission to manage Serve. The script does not elevate privileges.

After startup: `https://<node>.ts.net/` serves the application, `wss://<node>.ts.net:9443` serves validated signaling, and `https://<node>.ts.net:9444` serves Observer without sign-in. Client access is governed by tailnet ACLs/grants; verify TCP 443/9443/9444/7881 and UDP 7882 from each device.

| Host listener | Purpose |
|---|---|
| `127.0.0.1:18180` | Core HTTP |
| `127.0.0.1:18181` | Separate signal process, proxying only RTC join/validate |
| `127.0.0.1:18182` | Observer HTTP |
| `127.0.0.1:17880` | LiveKit API, local diagnostics only |
| `<Tailscale IPv4>:7881/tcp`, `:7882/udp` | WebRTC through Docker port forwarding |

Compose uses bridge networking and a separate LiveKit network namespace. Clients cannot access the API directly, and ICE advertises only the specified Tailscale IPv4 address. This refines the original host-networking proposal: LiveKit's TCP mux listens on a wildcard address, while managing the current host's firewall is not permitted. Docker bindings are restricted to loopback/Tailscale IP; an external client must also verify that other interfaces are inaccessible and inspect actual ICE candidates.

The Checkpoint image provides three commands: `serve`, `signal`, and `observe`, each running in a separate container. Signal preserves WebSockets during core restarts, validates the signed token and session, then replaces grants with minimal permissions. Core reconciles permissions with LiveKit once per second; promotions may appear after that delay and **do not enable** devices automatically. The LiveKit Server API is not directly accessible through public signaling.

Application state is stored in SQLite under `.dev/data/`. Refreshing restores a valid session; the server checks expiration, revocation, and restrictions. The access model is described in [Access](access.md).

### Recording in the main local environment

`dev.sh up` also builds and starts `recorder` with FFmpeg. The recorder uses host networking and the local signaling address `http://127.0.0.1:17880` because LiveKit advertises media candidates using the host's Tailscale IP. The recorder has no inbound ports. Core and recorder exchange jobs through a dedicated Unix socket; the recorder has no access to the account database or LiveKit keys. Separate tokens are created once under `.dev/media/` and are not logged. Repeated preparation preserves configuration and tokens.

Settings are in `.dev/media/minutes.yaml`, recordings in `.dev/recordings/`, recorder working tracks in `.dev/recorder-data/`, and the socket in `.dev/media-run/`. By default, recording without minutes, stopping, and archive playback are available. ASR/LLM models and the processor are not included in this minimal environment; the UI indicates that recording with minutes is unavailable. Limits: 2 simultaneous recordings, a 20 GiB archive, at least 2 GiB of free space, and continuation confirmation every 2 hours.

The recording button remains visible to the owner and admin even if the module is disabled or the recorder is unavailable; a tooltip explains the reason and starting is disabled. UI checks cover these states. Verification of the running environment includes actual start, stop, and audio playback from history, separately from isolated tests.

### Automated checks

`check.sh` requires Node 26.10.0/npm on the host; Go runs in a pinned Docker builder. Browsers run in a Playwright Docker image with its bundled libraries.

```bash
./scripts/check.sh
./scripts/integration.sh
# Interrupts only the test core/LiveKit; run without real users:
CHECKPOINT_TEST_RESTARTS=1 ./scripts/integration.sh
./scripts/browser.sh
# If HTTPS Serve is unavailable: temporary localhost profile, restored on exit.
# Recreates core/signal; does not test HTTPS or remote tailnet access.
CHECKPOINT_BROWSER_LOCAL=1 ./scripts/browser.sh
```

The integration test checks real RTP transmission, rejection of viewer publication, role promotion/demotion, rejoining with an old signed elevated token, Observer independence, and restarts. Browser tests use synthetic devices and check UI, video, screen sharing, mute/reconnect, and stale → Unknown transitions. These results do not replace testing with two physical devices.

`scripts/integration.sh` uses the Docker socket to manage only the `checkpoint-dev` project; do not run it from an untrusted checkout. Restarts require an explicit flag. Keep test logs and screenshots in `.dev/`; remove secrets and identifiers before publishing them.

### Manual testing and cleanup

Test joining, audio, camera, screen sharing, roles, and reconnects on two physical devices, preferably on different networks. In `chrome://webrtc-internals` or `about:webrtc`, record the selected ICE pair, resolution/codec/bitrate, loss, and reconnects. For the overlay path, use `tailscale ping <test-device>` and `tailscale status` on participating devices to identify direct / peer relay / DERP routing. Do not copy the full tailnet address book or tokens into reports.

```bash
docker stats --no-stream checkpoint-dev-core-1 checkpoint-dev-signal-1 checkpoint-dev-livekit-1 checkpoint-dev-observer-1
./scripts/dev.sh logs
./scripts/dev.sh down
./scripts/dev.sh up
```

`down` removes only the three routes recorded in `.dev/routes.json` that still exactly match the created configuration. If another party has changed them, it stops with an explanation; it does not use a global Serve reset. The original snapshot is in `.dev/serve-before.json`. Project containers are removed, while test secrets and roles are preserved. Do not delete `.dev/` before removing routes and stopping the environment. The Docker socket is not mounted into application services.

## Functional and integration coverage

- Organizer and guest entry, invitation expiration/revocation, and the waiting room.
- HTTP and WebSocket authorization, including denial of access to unrelated rooms.
- Actual media-server rejection of viewer publication.
- Inviting speakers to the stage, removing them, kicking participants, and attempted reentry with old tokens.
- Application restarts, repeated/missing webhooks, and reconciliation with LiveKit.
- Concurrent moderation commands and competing screen-share attempts.

## Networks and browsers

Desktop Chrome/Edge, Firefox, and Safari in agreed current versions; mobile Safari/Chrome are tested separately for supported features. Do not promise mobile screen sharing without verification.

Scenarios include home networks, mobile networks, VPN, TCP/TLS only, packet loss and reordering, latency, network switching, laptop sleep, revoked permissions, and headset disconnection.

## Load

- Meeting: 20 camera publishers, audio, and screen sharing.
- Webinar: 100 viewers and 1–3 speakers, camera, and code sharing.
- Additional 300/500-viewer steps explore capacity and are not a v1 commitment.
- Record codec, bitrate, resolution, simulcast, subscription count, TURN share, and the screen-sharing scenario.
- Run the load generator separately from the VPS under test.
- Combine `lk load-test` with real browsers; synthetic results do not equal user experience.
- Measure CPU, RAM, network, loss, latency, join time, and reconnects; leave headroom below saturation.
- Report CPU specifications, vCPU type, network capacity, OS, and component versions.

LiveKit has published a reference result of 1 video publisher and 3000 subscribers on c2-standard-16 at 720p, using approximately 92% CPU and 531 MB/s outbound bandwidth. This is a separate synthetic test; do not scale it linearly to a low-cost VPS or treat it as Checkpoint validation.

## Installation, signatures, and update failures

- Incorrect DNS, occupied ports, insufficient disk space, and ACME failure.
- Repeated setup and installation on unsupported operating systems.
- Modified manifests/binaries, an untrusted key, incorrect architecture, and an old release sequence.
- Interrupted downloads, partially uploaded signatures, and asset replacement during signing.
- Unsafe archive paths/links and concurrent updates.
- Interruption at every update step, migration failure, and readiness failure.
- Key preservation during normal keygen, explicit replacement with force, file permissions, and absence of secrets in logs.
- Backup restoration and behavior after key replacement.

## Related documents

- [Reliability targets](architecture.md)
