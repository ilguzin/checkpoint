CREATE TABLE users (
 id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
 admin INTEGER NOT NULL CHECK(admin IN (0,1)), disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1))
);
CREATE TABLE logins (
 secret_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), expires INTEGER NOT NULL
);
CREATE TABLE rooms (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, mode TEXT NOT NULL CHECK(mode IN ('meeting','webinar')),
 owner_id TEXT REFERENCES users(id), closed INTEGER NOT NULL DEFAULT 0 CHECK(closed IN (0,1))
);
CREATE TABLE invites (
 id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL UNIQUE, room_id TEXT NOT NULL REFERENCES rooms(id),
 role TEXT NOT NULL CHECK(role IN ('moderator','presenter','attendee')),
 expires INTEGER NOT NULL, max_uses INTEGER NOT NULL CHECK(max_uses>=0), uses INTEGER NOT NULL DEFAULT 0,
 revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1))
);
CREATE TABLE sessions (
 identity TEXT PRIMARY KEY, name TEXT NOT NULL, room_id TEXT NOT NULL REFERENCES rooms(id),
 role TEXT NOT NULL CHECK(role IN ('owner','moderator','presenter','attendee')),
 can_publish INTEGER NOT NULL CHECK(can_publish IN (0,1)), expires INTEGER NOT NULL,
 secret_hash TEXT NOT NULL UNIQUE, invite_id TEXT REFERENCES invites(id), user_id TEXT REFERENCES users(id),
 revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1))
);
CREATE INDEX sessions_room ON sessions(room_id);
CREATE INDEX sessions_invite ON sessions(invite_id);
CREATE TABLE room_bans (
 room_id TEXT NOT NULL REFERENCES rooms(id), user_id TEXT NOT NULL REFERENCES users(id),
 PRIMARY KEY(room_id,user_id)
);
CREATE TABLE webhook_events (id TEXT PRIMARY KEY, digest TEXT NOT NULL, received INTEGER NOT NULL);
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
