# Accounts, rooms, and access

This document describes the current access model in SQLite schema 5, including shared rooms and recording history.

Account administration is available only through the local `checkpoint` CLI. The web interface is for joining meetings and managing an organizer's own rooms or rooms where they have explicitly been granted the Room Admin role. It provides no account creation, account directory, account disabling, or platform-wide administration. The interface supports English and Russian.

Create the first organizer and subsequent organizers with the same command:

```bash
# Save a 12–72-byte password to a private file using a password manager/editor.
chmod 600 /private/organizer-password
CHECKPOINT_DATABASE=/var/lib/checkpoint/checkpoint.db checkpoint users add \
  --username alex --password-file /private/organizer-password
```

Run as the `checkpoint` service user, or preserve that user's ownership of the database. Passwords must not appear in command arguments or shell history. The CLI accepts private regular files, rejects symbolic links, and allows a trailing LF or CRLF. It does not print passwords. New accounts are ordinary organizers; no separate bootstrap administrator is needed.

Local administration commands use the database selected by `CHECKPOINT_DATABASE`:

```bash
checkpoint users list                    # JSON account list, without password hashes
checkpoint users disable --username alex
checkpoint rooms list                    # JSON room list across all owners
checkpoint rooms close --id ROOM_ID
checkpoint users --help
checkpoint rooms --help
```

Disabling an account revokes its login and participant sessions, including accounts with the legacy administrator flag. Closing a room blocks joins and reconnects; the running service reconciles room/session state with the database. Service diagnostics are available through `checkpoint status` and `checkpoint doctor`.

`checkpoint admin init --username NAME --password-file FILE` remains compatible with old installation instructions and still rejects repeated initialization. A persisted legacy administrator flag grants no global web privileges. Use `users add` for new installations and additional accounts.

There are no public registration or account-bootstrap APIs. Passwords are hashed with bcrypt at cost 12; the permitted length is 12–72 bytes. Account cookies use HttpOnly, SameSite=Strict, and Secure over HTTPS, expire after 24 hours, and are stored as hashes in the database. State-changing requests, including login, require an exact Origin match. Signing out revokes the current session; changing a password revokes all account login sessions. The settings page is unavailable during a meeting: the user must leave before changing their password. This API does not revoke existing participant sessions; a local CLI password reset does.

## Room IDs and guest access

The entry screen offers account sign-in and guest access by room ID/link with a display name. Signed-in users see tiles for their own rooms, rooms with an explicitly granted Room Admin role, and previously visited shared rooms. The workspace supports name search, ownership filters, creation, and entry by room ID/link. No account has a global web directory of rooms. Rooms are created with a name and an immutable Meeting/Webinar mode.

New IDs contain 10 cryptographically random bytes, encoded as 16 Crockford base32 characters grouped as `XXXX-XXXX-XXXX-XXXX` (80 bits). Case, spaces, and hyphens are ignored; `O` is accepted as `0`, and `I`/`L` as `1`. Existing IDs remain valid. An ordinary link is `/#room=ROOM_ID`. Knowing the ID/link grants guest access: no additional account or individual approval is required unless the owner has enabled the waiting room. These links have no use counter or expiration; a closed room or locked guest admission blocks entry. Diagnostic rooms are excluded.

Guest sessions last at most 12 hours. Reconnects use the session secret. Guests can publish media in Meeting mode; in Webinar mode they can only subscribe until promoted by a host. Knowing an ID does not grant ownership or Room Admin rights. Once an account is admitted, the room is saved under **Shared with me**. A name and ID cannot impersonate a host: Owners/Admins join through an account.

Legacy `/#invite=` links retain their role, expiration, use limit, and revocation rules. Only a hash of the secret is stored. An expired or revoked invitation does not become privileged access. Secrets are in the URL fragment, which is not sent in HTTP requests or referrers; the app removes the fragment from the address bar.

## Room Owner and Room Admin

Only the owner can create a **Room Admin invitation**, shown once as `/#adminInvite=SECRET`. It lasts 24 hours and can be accepted by one existing signed-in account. The token is stored as a hash; acceptance atomically consumes it and persists the account-to-room grant. The owner cannot accidentally consume their own link. Explicit acceptance is required after sign-in. The account itself is created through the CLI, not through this link.

| Action | Owner | Room Admin | Guest |
|---|---|---|---|
| Room appears in the account workspace | Yes | Yes | No |
| Share an ordinary link / ID | Yes | Yes | Anyone who already knows the link |
| Join as host, admit guests, promote/demote speakers, answer questions | Yes | Yes | No |
| Grant/revoke Room Admin or create moderator invitations | Yes | No | No |
| Close the room, end the meeting for everyone, remove a participant | Yes | No | No |
| Change guest-admission policy or revoke an individual invitation | Yes | No | No |

Room Admin uses the internal participant role `moderator`. Removing the account-to-room grant revokes that account's host sessions; subsequent joins and reconnects in that role are rejected. Other rooms and ordinary guests are unaffected. A former admin may join as an ordinary guest if they know the ID of a room that permits entry: the ID is an independent access secret. Legacy moderator-invitation APIs do not let an admin bypass owner-only role assignment.

Removing an anonymous guest ends the current session. If a legacy individual invitation was used, it is revoked as well. Someone who knows the ID can rejoin while guest access is open; the owner can control subsequent admission with the lock or waiting room. Permanent blocking of an anonymous person is not claimed.

The signal service opens SQLite with `mode=ro` and `query_only`, on a read-only filesystem mount. Every new connection checks session/room/invitation/account state and replaces incoming LiveKit grants with subscribe-only permissions. Core reconciles publishing rights at startup, once per second, and after signed webhooks. Invalid, expired, and revoked participants are disconnected; a database error postpones reconciliation rather than counting as revocation. Active signal WebSocket connections survive a core restart. Core downtime delays changes to connected participants; new connections continue to be checked against the database.

The official LiveKit library verifies webhook JWTs and body SHA-256 hashes. Event IDs and exact-body hashes are stored transactionally for seven days; different bodies with the same ID are rejected. Events do not grant rights themselves: duplicates or out-of-order delivery only trigger reconciliation against authoritative state. Periodic checks cover lost events and the gap between event persistence and reconciliation.

SQLite uses a private 0600 file, foreign keys, `FULL synchronous`, a five-second busy timeout, short immediate transactions, and DELETE journaling. This allows signal to use a read-only connection without writable WAL/shared-memory files. The schema version is stored in `PRAGMA user_version`; both services reject unknown newer schemas. Empty-database migration is transactional. Early-prototype JSON is neither replaced nor imported implicitly: stop core, explicitly configure the legacy invitations, and run `checkpoint import-json /path/to/sessions.json` with CHECKPOINT_DATABASE set. The import is atomic, requires an empty sessions table, preserves expiration/identity/secret hashes, and leaves the source file unchanged. Old sessions did not record their invitation, so that relationship cannot be reconstructed; expiration and participant removal still work.

Password operations are limited to 10 requests per minute per network peer and 60 in total. Forwarded headers are not trusted, so this limit is shared behind Nginx. Additional per-client limits can be configured at the edge proxy. This is a single-server baseline without SSO, MFA, or a full identity-management/audit system.

## Compatibility and maintenance

Migration 003 adds `room_admins` and `room_admin_invites` without changing room IDs or clearing data. Owner/account sessions are rechecked against actual ownership; the old platform-admin flag no longer grants access to other users' rooms. Before upgrading from schema 2, take a consistent private SQLite backup and retain the old binary. Update core and signal in one maintenance operation, then verify account login, room lists, and guest reconnects. A schema-2 signal binary must not remain running after the upgrade. `checkpoint update` deliberately rejects automatic schema-changing upgrades; a tested migration and rollback procedure is required.

On a native installation, `checkpoint users list`, `rooms`, and other CLI commands automatically select `/var/lib/checkpoint/checkpoint.db` using the installation marker. Run them locally as root or the checkpoint user. `CHECKPOINT_DATABASE` takes precedence; containers without a native installation retain `/data/checkpoint.db`.

A local operator can reset a specific user's password with `checkpoint users set-password --username NAME --password-file FILE`. The normal policy remains 12–72 bytes. Explicit `--allow-short-password` permits 8–11 bytes only for that local reset; web password changes and account creation keep the normal minimum. A reset revokes the user's previous account and participant sessions.

## Shared rooms and recording history

[Specification](meeting-minutes.md)

After a signed-in account is successfully admitted by ID/invitation, ordinary membership is persisted and the room appears under **Shared with me**. Opening a link, being denied entry, or waiting for admission does not create membership. This is distinct from Room Admin. Each room appears once, with the user's highest currently valid role.

Room access permits reading and downloading **all retained recordings, transcripts, and minutes**, including material from before the first visit. History opens without joining the media session. Admitted anonymous guests receive room-scoped archive sessions under the same admission policy; a persistent cross-device list requires an account. Anonymous access history must not be attached to an account by matching display names.

Owners and Room Admins can start/stop/extend recordings, request minutes at recording start or for a saved recording, retry processing, cancel queued/active processing with explicit confirmation, edit minutes, and mark a revision as reviewed. Cancellation preserves the recording, completed transcript, and previous minutes; automatic retries stop and regeneration must be requested explicitly. Ordinary participants/guests have read-only access. Deleting material, closing/deleting rooms, revoking access, and assigning admins are Owner-only operations. Server settings and API keys are managed by the operator through the CLI.

**Room deletion** requires owner confirmation and deletes all room content. The active meeting ends, recording stops, jobs are cancelled, and invitations and sessions are revoked. Files, temporary tracks, transcripts, every minutes revision, and memberships are removed, with protection against late worker results. Deletion states, error handling, and backups are described in the [specification](meeting-minutes.md#deleting-a-room-and-all-its-content). Room Admins and participants cannot delete a room even through a direct API request.

Access is checked on every archive/list/download/Range request and every mutation. Closed rooms are hidden from Shared with me and unavailable to participants/guests; the Owner retains archive access until deletion. Archive access cannot bypass revocation, disabled accounts, session expiration, waiting rooms, or admission locks. Legacy invitation expiration/use/revocation limits remain binding and do not turn into permanent privileges. Knowing an ordinary ID still permits a fresh guest-admission request under the current policy; membership revocation alone does not revoke that independent secret. Ordinary membership is independent of Admin rights, so removing the admin role does not necessarily remove ordinary access.

Temporary participant audio is used for processing. Once the complete transcript and combined mix have been durably saved and verified, individual tracks and normalized copies are deleted. The combined mix, attributed/timestamped transcript, and minutes remain in history under an explicit retention policy. Completed minutes become available automatically, labeled as AI-generated and unreviewed; manual review is optional.
