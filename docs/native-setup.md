# Native Checkpoint installation

[Operations](operations.md)

Setup installs the application on a clean VPS. This path accepts a build delivered by the operator over a trusted channel. The signed bootstrap is described in [Releases](releases.md). The VPS does not need Docker, Node.js, Go, or Tailscale.

## Installation from the official website

The domain for the VPS-hosted website is still being chosen; replace `checkpoint.example` below with its address. After the first signed release, the recommended entry point is the public website, which serves a verified installer with a pinned key:

```bash
curl -fsSL https://checkpoint.example/install.sh -o checkpoint-install.sh
bash checkpoint-install.sh
```

The wizard asks for the IP address, domains (sslip.io is supported), SSH port, and an optional Certbot email address. It runs preflight checks by default. Until a signed release is available, the website explicitly says so; source installation is described below. See [release verification](releases.md).

## Requirements

- A clean Ubuntu 24.04 LTS host, amd64, systemd, Python 3, and at least 1 GiB of free disk space for installation. A test pilot should have 4 vCPUs, 8 GiB RAM, and 40–80 GiB SSD; capacity on a specific VPS has not yet been measured.
- A public IPv4 address assigned directly to a VPS interface. This profile does not support NAT or IPv6 DNS.
- Four distinct domains with A records pointing to that IP: `meet`, `rtc`, `turn`, and `status`. Remove AAAA records and disable CDN/HTTP proxies for these names.
- Root SSH access; specify the actual SSH port. There is no need to disable key authentication or change SSH settings.
- Allow inbound TCP on the SSH port, 80, 443, and 7881, and UDP on 7882 in the provider firewall. Outbound traffic must allow DNS, apt, GitHub, and ACME.
- Existing Nginx or Certbot configurations and active/configured third-party firewalls are unsupported. An inactive stock UFW installation without custom rules is allowed. Setup refuses to overwrite files it does not own. Manual integration is described in [Operations](operations.md); automatic merging of existing configurations is unsupported.

The installer configures UFW to deny incoming/routed traffic and allow outgoing traffic, the specified SSH port, and the public ports above. Standard UFW rules preserve loopback, established connections, ICMP, and DHCP; other incoming packets are blocked. IPv6 filtering is enabled. Inspect rules with `sudo ufw status verbose`. Setup does not change SSH configuration or keys. The LiveKit API, internal TURN listener, and relay ports are not exposed to external clients.

## Preparing a build

On a workstation with Docker and Git:

```bash
./scripts/build-native.sh
```

The output is `.dev/native/checkpoint-linux-amd64.tar.gz`: a static Go binary with the embedded UI, a manifest containing the commit/version/file SHA-256 hashes, and available license texts. A `-dirty` version suffix explicitly identifies a build with local changes. For a reproducible VPS test, build a clean commit and retain the printed artifact checksum.

Transfer the artifact to the VPS over a trusted SSH connection, for example:

```bash
scp .dev/native/checkpoint-linux-amd64.tar.gz root@VPS:/root/
scp scripts/setup.sh root@VPS:/root/checkpoint-setup.sh
```

Once the corresponding commit is published, `setup.sh` can be downloaded separately; it is self-contained. Replace `COMMIT` with a specific 40-character commit from this repository:

```bash
curl -fSL https://raw.githubusercontent.com/ilguzin/checkpoint/COMMIT/scripts/setup.sh \
  -o checkpoint-setup.sh
```

In this manual workflow, the operator supplies the Checkpoint artifact. Hash verification within the archive detects corruption but does not replace a signature or a trusted delivery channel. LiveKit 1.13.11 is downloaded from its official GitHub Release and verified against the SHA-256 pinned in the installer. Use `--livekit-archive /root/livekit.tar.gz` for an archive transferred in advance; the same checksum verification applies.

## Installation

As root on the VPS, first run preflight checks without making changes:

```bash
bash /root/checkpoint-setup.sh \
  --bundle /root/checkpoint-linux-amd64.tar.gz \
  --public-ip YOUR_PUBLIC_IPV4 \
  --app-domain meet.example.com \
  --rtc-domain rtc.example.com \
  --turn-domain turn.example.com \
  --observer-domain status.example.com \
  --email operator@example.com \
  --ssh-port 22
```

Then repeat the same command with `--apply`. This flag authorizes package installation, service creation, the specified firewall configuration, and acceptance of the ACME terms for certificate issuance. The installer:

1. Checks the platform, DNS, local IP, SSH port, listeners, existing files/firewalls, and bundle contents.
2. Verifies the pinned LiveKit archive and creates persistent secrets and a file ownership record.
3. Installs Nginx with its stream module, Certbot, CA certificates, and UFW from Ubuntu repositories.
4. Creates unprivileged users and services for core, signal, LiveKit, and Observer; enables the firewall before starting network services.
5. Starts a separate `checkpoint-edge` Nginx service with an HTTP-01 webroot. The packaged `nginx.service` remains disabled.
6. Issues one SAN certificate for all four names and enables HTTPS/WSS/TURN TLS and the renewal timer.
7. Checks local health endpoints and HTTPS for the application and Observer, then prints URLs and diagnostics.

A standard VPS installation does not create test invitations. Accounts are created locally through the CLI. As root on the VPS, prepare a private password file (12–72 bytes), then run the command as the service user:

```bash
python3 - <<'ADMIN_PASSWORD'
import getpass, os, pwd
path = '/var/lib/checkpoint/.initial-password'
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as output:
    output.write(getpass.getpass('Organizer password: '))
user = pwd.getpwnam('checkpoint')
os.chown(path, user.pw_uid, user.pw_gid)
ADMIN_PASSWORD
runuser -u checkpoint -- env CHECKPOINT_DATABASE=/var/lib/checkpoint/checkpoint.db \
  checkpoint users add --username organizer --password-file /var/lib/checkpoint/.initial-password
rm /var/lib/checkpoint/.initial-password
```

After success, sign in and create a room and invitations. Store the password in a password manager. If bootstrap fails, resolve the cause before attempting to create another account. `/etc/checkpoint/core.env` contains the LiveKit secret; do not publish it. Observer is accessible without signing in.

## Files and services

| Item | Purpose |
|---|---|
| `/opt/checkpoint/releases/<build>-<hash>/` | Checkpoint, manifest metadata in setup-state, and license texts |
| `/opt/checkpoint/current` | Active build; `/usr/local/bin/checkpoint` points to its binary |
| `/opt/checkpoint/livekit/1.13.11/` | Verified LiveKit and its LICENSE |
| `/etc/checkpoint/setup-state.json` | Artifact, parameters, secrets, and managed file hashes; root, `0600` |
| `/etc/checkpoint/core.env`, `observer.env` | Root-only environment files read by systemd |
| `/etc/checkpoint/livekit.yaml` | Root + LiveKit group, `0640` |
| `/var/lib/checkpoint/checkpoint.db` | SQLite accounts, rooms, invitations, and sessions; core, `0600` |
| `/etc/checkpoint/nginx.conf` | Separate edge configuration |
| `/etc/ufw/user.rules`, `/etc/ufw/user6.rules` | UFW rules managed through the CLI |
| `/etc/letsencrypt/live/checkpoint/` | Certbot certificate |
| `/etc/letsencrypt/renewal-hooks/deploy/checkpoint` | Nginx validation and graceful reload after renewal |

Services: `checkpoint-core`, `checkpoint-signal`, `checkpoint-observer`, `checkpoint-livekit`, `checkpoint-edge`, and `ufw`. All start at boot; `certbot.timer` is also enabled.

Core and signal share a UID. systemd mounts SQLite read-only for signal, and its connection uses mode=ro/query_only. DELETE journaling requires no WAL/shm writes. Observer runs as a separate user, stores history in `/var/lib/checkpoint-observer/observer.db`, and has no access to the core database.

## HTTPS and TURN

Nginx accepts TCP 443 on the public IPv4 address. SNI routes application, signaling, and Observer traffic to the loopback HTTPS listener on `8443`, and TURN to the loopback TLS stream listener on `8444`. Nginx terminates TLS; decrypted TURN traffic with a trusted PROXY header is forwarded to LiveKit on `5349`. The header preserves the client address, including for browsers that reject a loopback mapped address.

LiveKit 1.13.11 advertises its built-in `turns:<domain>:443?transport=tcp` regardless of the internal `tls_port`. This was checked against the [ICE server construction source](https://github.com/livekit/livekit/blob/v1.13.11/pkg/service/roommanager.go); see the [TURN source](https://github.com/livekit/livekit/blob/v1.13.11/pkg/service/turn.go) for `external_tls` and PROXY header handling. Do not reuse this configuration with another version without verification.

The LiveKit API on `7880` and Checkpoint backends on `18180–18182` listen on loopback. The host firewall blocks external access to TURN `5349` and relay ports `30000–30100`; the built-in TURN server relays media to the same SFU. The LiveKit management API is not proxied to the public rtc domain.

Certificate renewal triggers a graceful reload of Nginx only. LiveKit and signal are not restarted; existing TCP connections continue to be served by the previous Nginx workers. Long calls may keep these workers alive until their connections close.

## Reruns and failures

Rerun with the same artifact and arguments: invitations, API secrets, roles, and data are preserved. Running core, signal, and LiveKit services are not restarted; Nginx performs a graceful reload. An existing certificate does not need to be reissued.

Setup does not replace the updater: `checkpoint update` handles signed compatible versions. A different build, changed domains/IP/SSH port, or manual edits to managed files cause setup to refuse. Setup stores SHA-256 hashes of the files it creates and does not overwrite manual edits. `checkpoint.db` data is outside this list and can change freely through the application.

On failure, installation stops without deleting data. Fix the cause and rerun with the same artifact. For example, an ACME failure leaves saved secrets and the HTTP-01 listener in place. There is no full transactional rollback of packages or host state; a crash during a write may require comparing a file against setup-state. Do not delete setup-state to force reinstallation.

## VPS verification

```bash
checkpoint version
systemctl status checkpoint-core checkpoint-signal checkpoint-livekit checkpoint-observer checkpoint-edge
journalctl -u checkpoint-core -u checkpoint-signal -u checkpoint-livekit -u checkpoint-edge
certbot renew --dry-run --run-deploy-hooks
ufw status verbose
ufw show added
ss -lntup
```

After installation and a setup rerun, compare private configuration hashes and data, reboot the VPS, and verify automatic startup. During a renewal test, keep direct and TURN calls active; record increasing received RTP/frame counts and the absence of reconnects.

From two external networks without Tailscale, test sign-in, camera, microphone, screen sharing, roles, reconnects, and the separate Observer site. Seeing a TURN URL in SDP is insufficient to verify fallback: force `iceTransportPolicy: relay` or block the direct media path on the client, then inspect the **selected** ICE pair (`candidateType=relay`, `relayProtocol=tls`, URL `turns:...:443?transport=tcp`) and incoming RTP. Verify that `7880`, `5349`, `8443–8444`, `18180–18182`, and relay ports are closed externally.

Browser scenarios are in `web/e2e/native.spec.ts`. For a VPS, create a private JSON file containing `publicURL`, `observerURL`, `rtc`, `turn`, and `meeting`; pass its path through `CHECKPOINT_NATIVE_ACCESS` and set `CHECKPOINT_NATIVE_TEST=1`. Run `playwright test native.spec.ts` without `CHECKPOINT_NATIVE_INSECURE`: the browser must validate the public certificate. Do not store invitations in Git.

The local `./scripts/native-smoke.sh` runs native binaries and Nginx in a separate Ubuntu 24.04 container, with browsers in another container. It does not change the tailnet environment. Only the server container receives `NET_ADMIN`: real UFW rules are applied and reapplied in its own network namespace, and internal ports are checked from the second container. The test uses `.test` names and a temporary self-signed certificate with verification explicitly disabled **only for this local test**. For a private Docker IP, the test adds a narrow exception to the TURN peer policy; the production profile accepts only globally routable IPv4 addresses. This check does not replace testing a public VPS, ACME, systemd boot, the provider firewall, or physical external networks.

Certbot can register without an email address: pass `--no-email` instead of `--email` to `scripts/setup.sh`. In a YAML target, set `spec.tls.noEmail: true` and omit `email`. These options are mutually exclusive; certificate issuance and automatic renewal work in both modes.
