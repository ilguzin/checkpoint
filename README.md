# Checkpoint

**Sync up. Move forward.**

[![Status: pre-alpha](https://img.shields.io/badge/status-pre--alpha-orange)](#status)
[![Self-hosted](https://img.shields.io/badge/deployment-self--hosted-2ea44f)](docs/operations.md)
[![Go](https://img.shields.io/badge/backend-Go-00ADD8?logo=go&logoColor=white)](docs/architecture.md)
[![React + TypeScript](https://img.shields.io/badge/frontend-React%20%2B%20TypeScript-3178C6?logo=react&logoColor=white)](docs/architecture.md)
[![LiveKit](https://img.shields.io/badge/media-LiveKit-111827)](docs/architecture.md)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)
[![Checks](https://github.com/ilguzin/checkpoint/actions/workflows/check.yml/badge.svg)](https://github.com/ilguzin/checkpoint/actions/workflows/check.yml)

Checkpoint (CP) is a meeting and webinar application for development teams, hosted on your own Linux VPS. It focuses on reliable calls, a clear interface, and straightforward operations.

## Status

Pre-alpha: meetings, webinars, audio recording, and meeting minutes are available. The first public signed release is still in preparation. Long-running load tests and the full matrix of real mobile devices remain incomplete; production v1 readiness is not claimed.

The website and installer will be hosted on the project owner's VPS; the public domain is still being selected. Until the first signed release, build from source and transfer the bundle to your VPS through a trusted channel.

## Features

- **Meeting:** device selection and preview, video, one screen share enforced by the server, chat, raised hands, waiting room, admission lock, and moderation.
- **Webinar:** stage and audience, questions, co-hosts, speaker invitations, and revocation of publishing rights. Participants enable their own camera and microphone.
- **Access:** organizer accounts managed through the CLI, persistent rooms, guest entry by room ID/link, shared rooms, and delegated Room Admin access. Individual invitations also support expiration, use limits, and revocation.
- **Interface:** a responsive workspace, focused speaker view, integrated device menus, and participants grouped by role with an ordered raised-hand queue.
- **Observer:** a separate public website without sign-in, check and incident history, measurement coverage, and a real synthetic-audio delivery test in the browser.
- **Operations:** native setup, systemd, Nginx/Certbot, status/doctor, consistent backup/restore, signed updates, and interrupted-update recovery.
- **YAML deployment:** installation and security policy over root SSH, with known_hosts verification, an independent second SSH connection, and a rollback timer.

Owners and Room Admins can record audio with or without meeting minutes. The combined mix, transcript, and minutes are available in room history. Previously visited rooms appear under **Shared with me**. Local models require a separate setup; operator-configured API adapters are also supported. [Recording and limitations](docs/meeting-minutes.md).

The interface supports English and Russian. Language and password settings are on a separate page outside meetings; automatic language selection follows browser preferences.

## Architecture and distribution

One Go binary with an embedded React UI runs as core, signal, and Observer. The main SQLite database stores access and rooms; Observer uses a separate SQLite database for history. Official LiveKit 1.13.11 runs as a separate process. Nginx and Certbot come from Ubuntu packages. Docker, Node.js, and Go are not required on the user's VPS.

The first version uses **one server**. Stopping core should not interrupt established media streams; a failure of the entire VPS still takes down the service. Multi-server redundancy is not supported yet.

## Running and testing

For the local environment, with Docker/Compose, Python 3, and Tailscale configured:

```bash
git clone https://github.com/ilguzin/checkpoint.git
cd checkpoint
./scripts/dev.sh up
./scripts/dev.sh links
```

The local environment includes a recorder with FFmpeg, providing recording start/stop and an archive. Minutes generation requires separate model and processor configuration; the minutes option remains unavailable until then. Recording configuration: `.dev/media/minutes.yaml`; files: `.dev/recordings/`.

This starts a development environment, not a public VPS installation. Use a printed invitation to join as a guest; creating and managing rooms requires an organizer account created through the [local CLI](docs/access.md).

`links` prints private test invitations. Do not publish them. Old JSON sessions are not imported automatically: see the [migration instructions](docs/access.md).

Checks on a workstation with Node/npm and Docker:

```bash
./scripts/check.sh
./scripts/build-native.sh
./scripts/native-smoke.sh
```

Native smoke starts isolated containers with real binaries, Nginx, UFW, and a browser. It tests direct WebRTC, TURN TLS 443, roles, and UI without reconfiguring the running tailnet environment. [Load tests](docs/load-testing.md) run separately.

The supported VPS profile is a clean **Ubuntu 24.04 LTS amd64 server with a direct public IPv4** and four hostnames. [Native setup](docs/native-setup.md) covers preflight, installation, and account creation; [deployment](docs/deployment.md) covers target YAML. The `scripts/install.sh` template deliberately refuses to run without the owner's key: the public bootstrap must first be built and published using the [release procedure](docs/releases.md).

Short local runs passed for Meeting 20 and Webinar 100 viewers + 3 hosts. These test transport on one host and **do not establish VPS capacity**. One-hour/four-hour runs and a real pilot remain outstanding.

## Documentation

| Document | Contents |
|---|---|
| [Meeting minutes](docs/meeting-minutes.md) | Recording, individual tracks, minutes template, and room history |
| [Architecture](docs/architecture.md) | Components, data, networking, HTTPS, and TURN |
| [Access](docs/access.md) | Accounts, invitations, roles, and storage |
| [Operations](docs/operations.md) | CLI, backup/restore, and diagnostics |
| [VPS deployment](docs/deployment.md) | YAML, SSH, and security |
| [Releases](docs/releases.md) | Local keys, signatures, updater, and limitations |
| [Observability](docs/observability.md) | Public measurements and history |
| [Testing](docs/testing.md) | Test matrix and tailnet environment |
| [Load testing](docs/load-testing.md) | Reproducible local runs and limitations |
| [Licensing](docs/licensing.md) | Dependency redistribution terms |

## License

Checkpoint's own code is licensed under [Apache-2.0](LICENSE). Dependency license texts are included in the native bundle and available through `checkpoint licenses` and `/licenses`. The catalog does not replace a final review of the contents of each public release.
