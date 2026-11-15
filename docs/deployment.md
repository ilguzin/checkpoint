# YAML deployment

[Native setup](native-setup.md)

The commands support one clean Ubuntu 24.04 amd64 VPS with a direct public IPv4. Signed deployment requires a published release and a pinned public key. Until the first such release, use manual bundle delivery as described in the native setup guide.

## Preparation

1. Copy the [example target](../deploy/targets/vps.example.yaml) to a private location outside Git. Replace the address, four hostnames, email, published signed-release version, SSH identity/known_hosts paths, and permitted operator CIDRs.
2. Add the public SSH key to root's authorized_keys on the VPS. Verify the host fingerprint through the provider console and add the trusted key to the specified known_hosts file. Do not automatically trust ssh-keyscan output.
3. Prepare four DNS A records without AAAA/CDN. Allow TCP SSH/80/443/7881 and UDP 7882 in the provider firewall.
4. Use a workstation binary with the release public key pinned. Deployment does not need the private signing key.

```bash
checkpoint deploy plan /private/team-vps.yaml
checkpoint deploy apply /private/team-vps.yaml
checkpoint deploy recover /private/team-vps.yaml
```

`plan` validates local YAML and displays changes/external actions without connecting to the server. `apply` verifies the pinned signed GitHub release, connects through OpenSSH with BatchMode/StrictHostKeyChecking, runs preflight, and invokes the same setup. Unknown fields, multi-server topology, unsupported platforms, and unsafe policies are rejected. Secret values are not stored in YAML.

## Security policy

Services use separate unprivileged users and private configuration/database files. SSH uses keys, with root prohibit-password; authorized_keys is preserved. SSH access is restricted to operator CIDRs, which must include the actual source address of the current SSH connection. Before applying changes, sshd configuration and UFW state are checked. A persistent timer rolls back SSH/firewall changes after 120 seconds unless an independent second connection confirms access. `recover` executes the saved rollback when SSH remains available; complete loss of access requires the provider console.

The deployment configures unattended security updates without automatic reboot and daily private backups with 1–30 days of retention. System-package updates may still restart their services; validate this during the pilot and agree on a maintenance window. System journald rotation currently follows OS policy.

Repeating the same target/release preserves data and secrets. Unrelated files and modified managed settings are not overwritten. Use `checkpoint update` to change versions; deploy is not a general-purpose engine for modifying existing configuration. CIDR/retention changes require a separate review of current files; automatic policy migration is not implemented.

After applying: create an account with `checkpoint users add`, then verify external direct ICE and forced TURN, private internal ports, renewal, backup/restore, and reboot. Local UFW state does not establish provider-firewall correctness. Validation of the complete signed release → SSH → systemd rollback chain remains outstanding.

Certbot can register without email: pass `--no-email` instead of `--email` to `scripts/setup.sh`. In target YAML, set `spec.tls.noEmail: true` and omit `email`. These options are mutually exclusive; certificate issuance and automatic renewal are available in either mode.

Set the firewall through `spec.security.firewall.provider: ufw`. Setup uses only the UFW CLI; it does not create a separate nftables table or checkpoint-firewall service. Automatic rollback saves/restores UFW configuration and runs `ufw reload`. Manual rule changes through UFW are allowed, but repeated deploy/setup stops if they differ from recorded hashes: reconcile changes with the target first; there is no automatic overwrite.
