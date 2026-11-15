ALTER TABLE rooms ADD COLUMN deletion_state TEXT NOT NULL DEFAULT '';
CREATE TABLE room_memberships (
 room_id TEXT NOT NULL REFERENCES rooms(id), user_id TEXT NOT NULL REFERENCES users(id),
 invite_id TEXT REFERENCES invites(id), created INTEGER NOT NULL DEFAULT (unixepoch()), revoked INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(room_id,user_id)
);
CREATE TABLE meeting_occurrences (
 id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id), generation INTEGER NOT NULL,
 started INTEGER NOT NULL, ended INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX occurrence_active ON meeting_occurrences(room_id) WHERE ended=0;
CREATE TABLE recording_runs (
 id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id), occurrence_id TEXT NOT NULL REFERENCES meeting_occurrences(id),
 request_key TEXT NOT NULL, requested_by TEXT NOT NULL, minutes_requested INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'starting', processing TEXT NOT NULL DEFAULT 'not_requested',
 created INTEGER NOT NULL DEFAULT (unixepoch()), started INTEGER NOT NULL DEFAULT 0, ended INTEGER NOT NULL DEFAULT 0,
 deadline INTEGER NOT NULL DEFAULT 0, window INTEGER NOT NULL DEFAULT 0, applied_window INTEGER NOT NULL DEFAULT 0,
 stop_reason TEXT NOT NULL DEFAULT '', empty_since INTEGER NOT NULL DEFAULT 0,
 profile TEXT NOT NULL, manifest TEXT NOT NULL DEFAULT '{}', mix_bytes INTEGER NOT NULL DEFAULT 0,
 partial INTEGER NOT NULL DEFAULT 0, recorder_lease TEXT NOT NULL DEFAULT '', recorder_until INTEGER NOT NULL DEFAULT 0,
 cleanup_done INTEGER NOT NULL DEFAULT 0,
 UNIQUE(room_id,request_key)
);
CREATE UNIQUE INDEX recording_active ON recording_runs(room_id) WHERE state IN ('starting','recording','finalizing');
CREATE TABLE processing_jobs (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES recording_runs(id), status TEXT NOT NULL DEFAULT 'queued',
 lease TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
 available_at INTEGER NOT NULL DEFAULT (unixepoch()), started INTEGER NOT NULL DEFAULT 0,
 cancel_requested INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX processing_active ON processing_jobs(run_id) WHERE status IN ('queued','transcribing','generating','cancelling');
CREATE TABLE recording_transcripts (run_id TEXT PRIMARY KEY REFERENCES recording_runs(id), body TEXT NOT NULL, created INTEGER NOT NULL DEFAULT (unixepoch()));
CREATE TABLE minutes_revisions (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES recording_runs(id), job_id TEXT, body TEXT NOT NULL,
 reviewed INTEGER NOT NULL DEFAULT 0, author TEXT NOT NULL, created INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE TABLE recording_audit (id INTEGER PRIMARY KEY, room_id TEXT NOT NULL, run_id TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL, action TEXT NOT NULL, created INTEGER NOT NULL DEFAULT (unixepoch()));
CREATE TABLE room_deletions (room_id TEXT PRIMARY KEY, requested INTEGER NOT NULL DEFAULT (unixepoch()), completed INTEGER NOT NULL DEFAULT 0);
CREATE TABLE media_workers (kind TEXT PRIMARY KEY, seen INTEGER NOT NULL, ready INTEGER NOT NULL DEFAULT 0);
