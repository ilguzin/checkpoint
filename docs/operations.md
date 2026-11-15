# Installation and operations

This document describes the system contract and operational limitations. Target metrics are not an SLA guarantee.

## Installation

[Setup](../scripts/setup.sh) supports a clean Ubuntu 24.04 amd64 host with a directly assigned public IPv4 address. See the [native installation guide](native-setup.md) for step-by-step commands, UFW and Nginx/Certbot configuration, and limitations.

There are two delivery methods: a source build transferred over trusted SSH, or a signed GitHub release through the public installer. The latter requires a published release and a pinned key; the current template deliberately refuses to run without a key. Neither path requires Docker on the VPS.

[Remote deployment](deployment.md) reads a [YAML target](../deploy/targets/vps.example.yaml), checks known_hosts, and applies the same installation using a root SSH key. One node is supported; secret values are not stored in YAML. The operator configures DNS, the provider firewall, and SSH access beforehand.

Rerunning setup preserves data and secrets. Unmanaged configurations and occupied ports cause refusal before files are overwritten. Manual integration with an existing web server is described below as a path requiring separate validation.

## First installation through the signed installer

1. The operator points DNS to the VPS and downloads the public setup script.
2. The script checks the OS, architecture, systemd, disk space, DNS, and port availability.
3. It downloads the signed manifest and required artifacts, verifying the signature, size, and hash before execution.
4. It installs system dependencies from trusted OS repositories.
5. It creates unprivileged service users, directories, and random secrets.
6. It configures Nginx, certificates, and systemd for the application, media server, and separate Observer, including the status site.
7. It runs local checks; the operator creates accounts with `checkpoint users add`.
8. It prints application and status URLs, the version, service states, and remaining network checks.

A localhost check alone cannot prove external UDP/TURN availability. The first browser connection should include diagnostics; CI uses a separate client machine.

## Files

| Path | Contents |
|---|---|
| `/opt/checkpoint/releases/<version>/` | Versioned distribution files |
| `/opt/checkpoint/current` | Link to the active distribution |
| `/usr/local/bin/checkpoint` | CLI entry point |
| `/etc/checkpoint/` | Configuration and server secrets |
| `/var/lib/checkpoint/` | SQLite and persistent state |
| `/var/backups/checkpoint/` | Backups under the selected policy |
| journald | Service logs |

The installer defines exact permissions and ownership. The web process runs without root. Privileged operations use explicit administrative CLI commands.

## Implemented CLI

```text
checkpoint serve
checkpoint signal
checkpoint observe
checkpoint version
checkpoint status
checkpoint doctor
checkpoint backup --output NEW_FILE --config-dir /etc/checkpoint
checkpoint restore --input BACKUP --destination NEW_DB --config-output NEW_DIR
checkpoint update --check
checkpoint update
checkpoint update --recover
checkpoint deploy plan TARGET.yaml
checkpoint deploy apply TARGET.yaml
checkpoint deploy recover TARGET.yaml
checkpoint licenses
```

Reinstallation is idempotent: it does not create another administrator, change existing secrets, or delete data. Data deletion is a separate explicit action, excluded from normal updates.

SQLite backups use a consistent snapshot/backup mechanism or suspend writes. Copying an open database file without accounting for WAL is not supported. Restore is verified on a clean VPS.

## Related documents

- [Networking and HTTPS](architecture.md)
- [Signed releases](releases.md)

## Current backup and restore procedure

On a native VPS, the CLI selects `/var/lib/checkpoint/checkpoint.db` automatically based on the setup marker; an explicit `CHECKPOINT_DATABASE` takes precedence. Without native setup, the development profile retains `/data/checkpoint.db`. `status` checks local HTTP endpoints; `doctor` also checks access to the private database and its schema version. These checks do not prove external UDP/TURN connectivity.

```bash
sudo env CHECKPOINT_DATABASE=/var/lib/checkpoint/checkpoint.db checkpoint doctor
sudo env CHECKPOINT_DATABASE=/var/lib/checkpoint/checkpoint.db checkpoint backup \
  --output /var/backups/checkpoint/manual-20261110.tar.gz --config-dir /etc/checkpoint
sudo checkpoint restore --input /var/backups/checkpoint/manual-20261110.tar.gz \
  --destination /var/backups/checkpoint/restored.db --config-output /var/backups/checkpoint/restored-config
```

Create parent directories in advance with mode 0700. Output files/directories must not already exist. Backup uses VACUUM INTO and a SHA-256 manifest; the 0600 archive contains private data. Restore checks archive contents, gzip integrity, hashes, and schema, and restores to a separate location. Before replacing the live database, stop core/signal, preserve the original database, validate the restored one, and restore `checkpoint:checkpoint` ownership and mode 0600. Restore configuration selectively after checking domains and paths; do not run setup over an existing installation without review.

This backup excludes Observer history, `/etc/letsencrypt` certificates, binaries, and SSH/provider configuration. Full disaster recovery requires a separate protected backup of these items and another test on a clean VPS. YAML deployment configures a daily timer; basic setup alone provides manual backups only. Off-site storage is not implemented.

## Manual integration with an existing web server

Automatic setup targets a clean VPS and refuses existing unmanaged Nginx/Certbot/firewall configurations. Automatic merging is not implemented. Use a separate VPS until this mode is supported; if manual integration is required, the operator must prepare and verify:

1. A shared TCP 443 SNI stream router: existing HTTPS names route to their internal TLS listener; the TURN domain routes to a separate TURN TLS listener. An HTTP reverse proxy alone cannot proxy TURN.
2. App/RTC/Observer vhosts and an HTTP-01 webroot on port 80, preserving existing vhosts and certificate management.
3. PROXY protocol on internal listeners, trusting only the local edge, with internal ports blocked externally.
4. A single owner for certificate management and graceful reloads; an active TURN call must be tested during reload.
5. Separate UIDs, systemd units, and SQLite directories, plus coordinated firewall rule merging without flushing unrelated tables.

This is a runbook for reviewing the design, not a validated alternative installer. Full acceptance testing on an existing server remains outstanding.

## Recording setup and management

After installing native Checkpoint:

    sudo python3 /opt/checkpoint/current/scripts/minutes-setup.py
    sudo checkpoint minutes doctor
    sudo checkpoint minutes status
    sudo checkpoint minutes config show
    sudo checkpoint minutes config validate --file /root/minutes.yaml
    sudo checkpoint minutes config apply --file /root/minutes.yaml

Setup installs FFmpeg, private recorder/processor/generator services, a Python venv, and models verified with SHA-256. Docker is not required on the VPS. Use `--record-only` for recording without local processing; `--prepare-only` prepares services without restarting core. Rerunning setup requires no active recordings. Existing settings are preserved unless `--config` is explicitly supplied. CLI and YAML use the same profile; see [minutes.example.yaml](../deploy/minutes/minutes.example.yaml).

The initial installation downloads several gigabytes of models and builds llama.cpp from pinned sources using two build jobs; this can take tens of minutes on a VPS. Wait for setup to finish, then check `checkpoint minutes doctor` and `systemctl is-active checkpoint-recorder checkpoint-processor checkpoint-generator`. Model files and intermediate build output are retained for reruns; the generator's web UI is not installed.

The generator runtime is compiled for the VPS's actual CPU; AVX2 is not assumed to be available. When moving to a machine with a different instruction set, rebuild the runtime on the new host instead of copying the existing llama directory.

Native setup supports standard storage/socket paths; custom paths require matching systemd units. The generator listens only on 127.0.0.1:18089 and requires a private key. Core archives are stored in /var/lib/checkpoint/recordings; recorder/processor temporary directories are isolated. Minutes and transcripts are stored in SQLite. Models and runtime files in /opt/checkpoint-minutes are not included in every backup.

For an API adapter, specify adapter, endpoint, model, revision, and credentialFile. Only the required process should be able to read the key file; do not put the key value in YAML or Git. There is no hidden cloud fallback. Changes apply to new recordings; existing ones retain their configuration snapshot. Changing storage/socket paths requires a separate offline migration.

With recording enabled, backups include the combined mix, manifests, and SQLite, excluding individual audio tracks. The archive limit is 64 GiB and 1024 files; exceeding either causes the command to fail. Restore requires the **current independent deletion ledger** to prevent an old backup from resurrecting deleted recordings:

    sudo checkpoint restore --input /var/backups/checkpoint/manual.tar.gz \
      --destination /var/backups/checkpoint/restored.db \
      --config-output /var/backups/checkpoint/restored-config \
      --deletion-ledger /var/lib/checkpoint/recordings/deletions

Restored media is placed beside the new database in restored.db.recordings, and the profile is bound to that path. Before bringing it online, check paths, UIDs, and systemd configuration, restore current settings, and stop the old processes. Pending capture is marked interrupted; processing may use the mix without precise speaker attribution. Back up the deletion ledger separately from old archives and retain it for at least their maximum retention period.

An invalid profile, missing worker, or unavailable generator does not stop normal calls. Failure to read the deletion ledger blocks core startup to avoid exposing previously deleted data. Doctor and Observer show module readiness and queue state without meeting text or room names.

Normal `checkpoint update` waits for active recordings/processing to finish and switches enabled recorder/processor services along with core. Services disabled by the operator are not enabled automatically; the generator has its own runtime and is not restarted when the core binary changes.
