# Signed releases and updates

[Operations](operations.md)

The local Minisign/Ed25519 signer, installer template, and Go updater are implemented. The owner's first production key and signed GitHub release have not yet been created. A development build without a pinned key refuses update/deploy operations. The repository is fixed to `ilguzin/checkpoint`; the current distribution targets Linux amd64.

## Local key

The owner's machine needs Minisign and an authenticated GitHub CLI (`gh`). The private key and its passphrase must not be sent to CI, GitHub, or the VPS. The script sets umask 077; by default, Minisign creates an encrypted key with an interactive passphrase prompt.

```bash
export CHECKPOINT_SIGNING_KEY_FILE="$HOME/.config/checkpoint/release.key"
python3 scripts/release.py keygen
```

An existing pair is preserved. If only the public key is missing, it is recovered from the private key. If only the public key remains, the script requires recovery of the private key or an explicit `keygen --force`. Force replaces the pair but **does not rotate trust on installed clients**. `sign` creates a missing key through the same mechanism; before release, verify that the build and installer contain the matching public key.

Store an encrypted key backup separately from the password manager holding its passphrase. Prepare recovery on another trusted machine before the first public release.

## Publishing a release

After reviewing and committing the source, from a clean checkout:

```bash
./scripts/check.sh
python3 scripts/licenses.py
# If the catalog changed, review and commit it before building the release.
export CHECKPOINT_RELEASE_PUBLIC_KEY="$(tail -n 1 "$HOME/.config/checkpoint/release.pub")"
CHECKPOINT_BUILD_VERSION=v0.1.0 ./scripts/build-native.sh
python3 scripts/release.py installer \
  --public-key "$HOME/.config/checkpoint/release.pub" --output .dev/native/install.sh
```

`CHECKPOINT_BUILD_VERSION` requires a clean Git checkout and a pinned public key. The bundle contains a static binary, setup.sh, LICENSE, third_party, and an internal manifest with commit/hash metadata. Archive timestamps are fixed; bit-for-bit reproducibility across different machines has not been demonstrated separately. The internal manifest is not a signature.

Create a **draft** release for the desired commit/tag and upload `checkpoint-linux-amd64.tar.gz` and the rendered `install.sh`. For example, `gh release create v0.1.0 --draft --target COMMIT --title 'Checkpoint v0.1.0' --notes-file release-notes.md .dev/native/checkpoint-linux-amd64.tar.gz .dev/native/install.sh`. Then run:

```bash
python3 scripts/release.py sign v0.1.0 --sequence 1
```

By default, the signer reads the schema from `core.SchemaVersion` in the current checkout (currently 5). Sign from the same checkout used to build the bundle; `--schema` is available for an explicitly verified different release. This does not authorize automatic migration of an existing database.

The signer downloads the actual assets through the GitHub API, limits their size/count, verifies digests, signs the exact release.json bytes, and verifies the signature locally. It checks asset IDs, sizes, and digests before and after uploading. Upload adds release.json and release.json.minisig without changing draft/published status. Existing signatures and immutable releases are not overwritten. Publish the draft after verification. Each subsequent release receives a higher sequence; reusing a sequence with different bytes is forbidden.

Setup downloads LiveKit 1.13.11 separately from its official release with a pinned SHA-256. The official archive may be included as an asset: the signer adds its hash to the manifest, but the current setup still uses the pinned upstream archive or an explicit `--livekit-archive`. Nginx and Certbot are OS packages.

## Initial installation and trust

The source `/scripts/install.sh` is a template with placeholders and deliberately refuses to run. Publicly distribute only a rendered installer with the key and schema from the same checkout as the bundle. The downloaded installer and its embedded key form the initial trust boundary; also publish the checksum/fingerprint through an independent channel.

The installer fetches the latest stable release and verifies Minisign, repository, version, schema, size, and hash before executing setup from the signed bundle. If Minisign is absent, installing the system verification package is allowed only with `--apply` as root on Ubuntu. The binary is also checked for a matching pinned key. On success, monotonic update-state is saved. Domain/IP/email/SSH parameters match [native setup](native-setup.md).

Do not use the installer as a general upgrade mechanism for a different existing build. Signatures do not replace the initial GitHub/TLS trust chain if an attacker replaces both the bootstrap and its key.

## Manifest v1 protocol

UTF-8 JSON: `format: 1`, `project: checkpoint`, `repository: ilguzin/checkpoint`, `channel: stable`, a positive `sequence`, `version: vMAJOR.MINOR.PATCH`, `livekitVersion`, `schema: {min,max,target}`, and `assets`.

Each asset has an exact basename, `os`, `arch`, `size`, `sha256`, and `kind` (`checkpoint`, `livekit`, or `installer`). The signature covers the original bytes of the entire document. Unknown fields/formats/platforms, duplicate assets, unsafe archive entries, incorrect hashes/sizes, downgrades, and sequence reuse are rejected. The release key in a new build must match the installed key.

## Updating and recovery

As root on the VPS:

```bash
checkpoint update --check
checkpoint update
checkpoint update --recover
```

1. Acquire an exclusive lock, fetch a stable GitHub release, and verify the signed manifest and asset.
2. Extract safely into staging, verify the candidate's public key, and save the versioned directory.
3. Maintenance blocks new joins; if LiveKit has active participants, defer the update and remove maintenance mode.
4. Create a consistent private database/configuration backup and durable journal, stop core/signal, switch the current symlink atomically, then start and check core/signal/Observer.
5. Save the accepted sequence and setup metadata before reopening joins. If startup fails, restore the old binary. After interruption, `--recover` chooses the version using durable state and the journal.

Rollback **does not restore an old database over new writes**. The current build uses schema 5 and LiveKit 1.13.11. The updater permits only updates within its build's schema: schema migrations and media server changes are rejected and require a separate maintenance deployment with a verified rollback plan. The updater does not interrupt active meetings; it refuses to proceed while busy. Automatic drain scheduling is not yet available.

If the candidate cannot start at all, run `update --recover` using the binary in the saved previous directory, `/opt/checkpoint/releases/…/checkpoint`: the maintenance helper uses that trusted executable. After a failure, follow the recovery message and preserve private journal/state/backup files. Do not delete update-state to bypass downgrade checks or manually replace current when its state is unclear. Compatible-version recovery has been tested with local SQLite and stub service hooks; full systemd/power-loss testing on a VPS remains outstanding.

## Key rotation and limitations

Automatic key rotation is not implemented. Planned rotation requires a separately verified transition using the existing trust chain. If a key is lost or compromised, stop publishing and reestablish trust on the VPS through an independent administrative SSH/console channel and a verified public key; a new GitHub link alone is insufficient proof. Signing with the old compromised key does not resolve the issue.

A signature proves byte authenticity, not freshness: hiding the latest release is not detected. TUF and distributed trust are not currently used. Dependency checks and release review remain part of publishing.

## Installer on the official website

The official website is hosted on the owner's VPS; its domain is still being chosen. `https://checkpoint.example/` below is an example, not a published site. Website source is maintained privately and deployed directly to the VPS; no separate public website repository is used. Installation from the website is unavailable until the first signed release.

The `scripts/install.sh` template remains in the application repository. The website build downloads the **exact installer from the published release**, verifies Minisign, hash, size, embedded key/schema/repository, and the presence of a signed bundle. Neither the website nor CI holds the private key. The installer is served at `/install.sh`; documentation and the website use this address rather than raw.githubusercontent.com.

```bash
curl -fsSL https://checkpoint.example/install.sh -o checkpoint-install.sh
bash checkpoint-install.sh
```

Without arguments, the wizard asks for a public IPv4 address, domains (sslip.io is supported), an SSH port, and an optional Certbot email address. It runs checks by default; installation requires explicitly choosing to apply changes. The wizard reads `/dev/tty`, so it also works when downloaded through a pipe, although a separate file is easier to inspect before execution.

`--help` works offline. Explicit automation arguments are described in the [installation guide](native-setup.md). `--verify-only --version v0.1.0` downloads and verifies a release without installing it; Minisign must already be installed. `--release-dir DIR` is permitted only with `--verify-only --version TAG` to verify a signed draft before publication.

The `release.yml` workflow builds an unsigned draft for a `vMAJOR.MINOR.PATCH` tag. The GitHub Actions variable `CHECKPOINT_RELEASE_PUBLIC_KEY` holds only the public key. The operator verifies passing CI for the intended commit, signs the draft locally, checks the installer, and publishes the release. Rerunning the signer completes a partially uploaded signature while preserving the existing manifest. Existing mismatched assets are not overwritten.
