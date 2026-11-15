CREATE TABLE room_admins (
 room_id TEXT NOT NULL REFERENCES rooms(id), user_id TEXT NOT NULL REFERENCES users(id),
 PRIMARY KEY(room_id,user_id)
);
CREATE TABLE room_admin_invites (
 id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL UNIQUE, room_id TEXT NOT NULL REFERENCES rooms(id),
 expires INTEGER NOT NULL, accepted_by TEXT REFERENCES users(id), revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1))
);
CREATE INDEX room_admin_invites_room ON room_admin_invites(room_id);
