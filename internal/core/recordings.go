package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ilguzin/checkpoint/internal/minutes"
)

const runSelect = `SELECT id,room_id,occurrence_id,state,processing,minutes_requested,started,ended,deadline,window,stop_reason,mix_bytes,partial,profile FROM recording_runs`

func scanRun(row rowScanner) (minutes.Run, minutes.Config, error) {
	var r minutes.Run
	var profile string
	var c minutes.Config
	e := row.Scan(&r.ID, &r.Room, &r.Occurrence, &r.State, &r.Processing, &r.MinutesRequested, &r.Started, &r.Ended, &r.Deadline, &r.Window, &r.StopReason, &r.MixBytes, &r.Partial, &profile)
	if e == nil {
		e = json.Unmarshal([]byte(profile), &c)
	}
	r.WarningSeconds = c.WarningSeconds
	return r, c, e
}
func audit(tx *sql.Tx, room, run, actor, action string) error {
	_, e := tx.Exec("INSERT INTO recording_audit(room_id,run_id,actor,action) VALUES(?,?,?,?)", room, run, actor, action)
	return e
}
func occurrence(tx *sql.Tx, room string, generation int) (string, error) {
	var id string
	e := tx.QueryRow("SELECT id FROM meeting_occurrences WHERE room_id=? AND ended=0 AND generation=?", room, generation).Scan(&id)
	if e == nil {
		return id, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	if _, e = tx.Exec("UPDATE meeting_occurrences SET ended=unixepoch() WHERE room_id=? AND ended=0", room); e != nil {
		return "", e
	}
	id = randomID()
	_, e = tx.Exec("INSERT INTO meeting_occurrences(id,room_id,generation,started) VALUES(?,?,?,unixepoch())", id, room, generation)
	return id, e
}
func (s *Store) startRecording(identity, key string, wants bool, c minutes.Config) (minutes.Run, error) {
	if !c.Enabled || len(key) < 16 || len(key) > 128 {
		return minutes.Run{}, errors.New("recording unavailable or missing idempotency key")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return minutes.Run{}, e
	}
	defer tx.Rollback()
	p, e := active(tx, identity, true)
	if e != nil || !p.Moderator {
		return minutes.Run{}, ErrAccess
	}
	previous, _, e := scanRun(tx.QueryRow(runSelect+" WHERE room_id=? AND request_key=?", p.Room, key))
	if e == nil {
		if previous.MinutesRequested != wants {
			return previous, errors.New("request key already used")
		}
		return previous, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return previous, e
	}
	var ready bool
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM media_workers WHERE kind='recorder' AND ready=1 AND seen>unixepoch()-15)").Scan(&ready); e != nil || !ready {
		return minutes.Run{}, errors.New("recorder not ready")
	}
	if wants {
		if e = c.CheckModels(); e != nil {
			return minutes.Run{}, e
		}
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM media_workers WHERE kind='processor' AND ready=1 AND seen>unixepoch()-15)").Scan(&ready); e != nil || !ready {
			return minutes.Run{}, errors.New("processor not ready")
		}
	}
	var count int
	if e = tx.QueryRow("SELECT count(*) FROM recording_runs WHERE state IN ('starting','recording','finalizing')").Scan(&count); e != nil {
		return minutes.Run{}, e
	}
	if count >= c.MaxConcurrentRecordings {
		return minutes.Run{}, errors.New("recorder capacity reached")
	}
	id, e := occurrence(tx, p.Room, p.Generation)
	if e != nil {
		return minutes.Run{}, e
	}
	b, _ := json.Marshal(c)
	r := minutes.Run{ID: randomID(), Room: p.Room, Occurrence: id, State: "starting", Processing: "not_requested", MinutesRequested: wants, WarningSeconds: c.WarningSeconds}
	_, e = tx.Exec(`INSERT INTO recording_runs(id,room_id,occurrence_id,request_key,requested_by,minutes_requested,profile) VALUES(?,?,?,?,?,?,?)`, r.ID, r.Room, id, key, p.Identity, wants, string(b))
	if e != nil {
		return r, e
	}
	if e = audit(tx, r.Room, r.ID, p.Identity, "recording_start"); e != nil {
		return r, e
	}
	return r, tx.Commit()
}
func stopRecording(tx *sql.Tx, id, reason string) error {
	_, e := tx.Exec("UPDATE recording_runs SET state='finalizing',stop_reason=?,ended=unixepoch() WHERE id=? AND state IN ('starting','recording')", reason, id)
	return e
}
func queueMinutes(tx *sql.Tx, id string) error {
	var count int
	if e := tx.QueryRow("SELECT count(*) FROM processing_jobs WHERE run_id=? AND status IN ('queued','transcribing','generating','cancelling')", id).Scan(&count); e != nil {
		return e
	}
	if count > 0 {
		return nil
	}
	if _, e := tx.Exec("INSERT INTO processing_jobs(id,run_id) VALUES(?,?)", randomID(), id); e != nil {
		return e
	}
	_, e := tx.Exec("UPDATE recording_runs SET processing='queued',minutes_requested=1 WHERE id=?", id)
	return e
}
func (s *Store) RecordingControl(room, id, actor, action string, window int, confirmation bool) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if !recordingManager(tx, actor, room) {
		return ErrAccess
	}
	r, c, e := scanRun(tx.QueryRow(runSelect+" WHERE id=? AND room_id=?", id, room))
	if e != nil {
		return ErrAccess
	}
	switch action {
	case "stop":
		e = stopRecording(tx, id, "manual")
	case "extend":
		now := time.Now().Unix()
		if r.State != "recording" || r.Window != window || now < r.Deadline-int64(c.WarningSeconds) || now >= r.Deadline {
			return errors.New("recording confirmation window expired or already extended")
		}
		var applied int
		if e = tx.QueryRow("SELECT applied_window FROM recording_runs WHERE id=?", id).Scan(&applied); e != nil {
			return e
		}
		if applied != window {
			return errors.New("previous extension not yet confirmed by recorder")
		}
		_, e = tx.Exec("UPDATE recording_runs SET deadline=deadline+?,window=window+1 WHERE id=?", c.ConfirmationSeconds, id)
	case "cancel":
		if !confirmation {
			return errors.New("confirmation required")
		}
		if r.Processing == "ready" {
			return errors.New("minutes already generated")
		}
		if _, e = tx.Exec(`UPDATE processing_jobs SET cancel_requested=1,status=CASE WHEN status='queued' THEN 'cancelled' ELSE 'cancelling' END WHERE run_id=? AND status IN ('queued','transcribing','generating','cancelling')`, id); e != nil {
			return e
		}
		_, e = tx.Exec(`UPDATE recording_runs SET processing=CASE WHEN EXISTS(SELECT 1 FROM processing_jobs WHERE run_id=? AND status='cancelling') THEN 'cancelling' ELSE 'cancelled' END WHERE id=? AND processing IN ('queued','transcribing','generating','cancelling')`, id, id)
	case "generate":
		if r.State != "saved" && r.State != "partial" {
			return errors.New("recording is not saved")
		}
		if r.Processing == "cancelling" {
			return errors.New("wait for cancellation")
		}
		if e = c.CheckModels(); e != nil {
			return e
		}
		e = queueMinutes(tx, id)
	default:
		return errors.New("unknown recording action")
	}
	if e != nil {
		return e
	}
	if e = audit(tx, room, id, actor, action); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) recordingHistory(room string) ([]minutes.Run, error) {
	rows, e := s.db.Query(runSelect+" WHERE room_id=? AND state!='deleted' ORDER BY created DESC,id DESC", room)
	if e != nil {
		return nil, e
	}
	out := []minutes.Run{}
	for rows.Next() {
		r, _, err := scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	e = rows.Err()
	rows.Close()
	return out, e
}
func (s *Store) recordingDetail(room, id string) (minutes.Run, error) {
	r, _, e := scanRun(s.db.QueryRow(runSelect+" WHERE room_id=? AND id=?", room, id))
	if e != nil {
		return r, e
	}
	var b string
	e = s.db.QueryRow("SELECT body FROM recording_transcripts WHERE run_id=?", id).Scan(&b)
	if e == nil {
		if e = json.Unmarshal([]byte(b), &r.Transcript); e != nil {
			return r, e
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return r, e
	}
	rows, e := s.db.Query("SELECT id,body,reviewed,created,author FROM minutes_revisions WHERE run_id=? ORDER BY created DESC,rowid DESC", id)
	if e != nil {
		return r, e
	}
	defer rows.Close()
	r.Revisions = []minutes.Revision{}
	for rows.Next() {
		var v minutes.Revision
		if e = rows.Scan(&v.ID, &v.Markdown, &v.Reviewed, &v.Created, &v.Author); e != nil {
			return r, e
		}
		r.Revisions = append(r.Revisions, v)
	}
	return r, rows.Err()
}
func (s *Store) editMinutes(room, id, actor, body string, reviewed bool, base string) error {
	if len(body) == 0 || len(body) > 128<<10 {
		return errors.New("invalid minutes length")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if !recordingManager(tx, actor, room) {
		return ErrAccess
	}
	var current string
	if e = tx.QueryRow("SELECT v.id FROM minutes_revisions v JOIN recording_runs r ON r.id=v.run_id WHERE r.id=? AND r.room_id=? ORDER BY v.created DESC,v.rowid DESC LIMIT 1", id, room).Scan(&current); e != nil {
		return e
	}
	if current != base {
		return errors.New("minutes changed; reload before editing")
	}
	if _, e = tx.Exec("INSERT INTO minutes_revisions(id,run_id,body,reviewed,author) VALUES(?,?,?,?,?)", randomID(), id, body, reviewed, actor); e != nil {
		return e
	}
	if e = audit(tx, room, id, actor, "minutes_revision"); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) DeleteRoom(user, room string, confirmed bool, storage string) error {
	if !confirmed {
		return errors.New("confirmation required")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var owns bool
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM rooms r JOIN users u ON u.id=r.owner_id WHERE r.id=? AND u.id=? AND u.disabled=0)", room, user).Scan(&owns); e != nil || !owns {
		return ErrAccess
	}
	// A separate fsynced ledger survives restoring an older database backup.
	if e = minutes.AtomicJSON(filepath.Join(storage, "deletions", hash(room)+".json"), map[string]any{"room": room, "requested": time.Now().Unix()}); e != nil {
		return e
	}
	if e = markRoomDeleting(tx, room); e != nil {
		return e
	}
	return tx.Commit()
}
func markRoomDeleting(tx *sql.Tx, room string) error {
	for _, q := range []string{
		"INSERT OR IGNORE INTO room_deletions(room_id) VALUES(?)",
		"UPDATE rooms SET deletion_state='deleting',closed=1 WHERE id=? AND deletion_state!='deleted'",
		"UPDATE sessions SET revoked=1 WHERE room_id=?",
		"UPDATE invites SET revoked=1 WHERE room_id=?",
		"UPDATE room_admin_invites SET revoked=1 WHERE room_id=?",
		"UPDATE recording_runs SET state='finalizing',stop_reason='room_deleted',ended=unixepoch() WHERE room_id=? AND state IN ('starting','recording')",
		"UPDATE processing_jobs SET cancel_requested=1,status=CASE WHEN status='queued' THEN 'cancelled' ELSE 'cancelling' END WHERE run_id IN (SELECT id FROM recording_runs WHERE room_id=?) AND status IN ('queued','transcribing','generating')",
	} {
		if _, e := tx.Exec(q, room); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) restoreDeletionLedger(storage string) error {
	entries, e := os.ReadDir(filepath.Join(storage, "deletions"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, e := os.ReadFile(filepath.Join(storage, "deletions", entry.Name()))
		if e != nil {
			return e
		}
		var marker struct {
			Room      string `json:"room"`
			Recording string `json:"recording"`
		}
		if e = json.Unmarshal(b, &marker); e != nil || marker.Room == "" {
			return fmt.Errorf("invalid deletion ledger %s", entry.Name())
		}
		tx, e := s.db.Begin()
		if e != nil {
			return e
		}
		if marker.Recording != "" {
			e = markRecordingDeleting(tx, marker.Room, marker.Recording)
		} else {
			e = markRoomDeleting(tx, marker.Room)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
		if e != nil {
			return e
		}
	}
	return nil
}

func recordingManager(tx *sql.Tx, actor, room string) bool {
	if canManage(tx, actor, room) {
		return true
	}
	p, e := active(tx, actor, true)
	return e == nil && p.Room == room && p.Moderator
}

func (s *Store) deleteRecording(room, id, actor string, confirmed bool) error {
	if !confirmed {
		return errors.New("confirmation required")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	owner := canOwn(tx, actor, room)
	if !owner {
		p, err := active(tx, actor, true)
		owner = err == nil && p.Room == room && p.Role == "owner"
	}
	if !owner {
		return ErrAccess
	}
	var profile string
	if e = tx.QueryRow("SELECT profile FROM recording_runs WHERE id=? AND room_id=?", id, room).Scan(&profile); e != nil {
		return ErrAccess
	}
	var config minutes.Config
	if e = json.Unmarshal([]byte(profile), &config); e != nil {
		return e
	}
	if e = minutes.AtomicJSON(filepath.Join(config.Storage, "deletions", "recording-"+id+".json"), map[string]string{"room": room, "recording": id}); e != nil {
		return e
	}
	if e = markRecordingDeleting(tx, room, id); e != nil {
		return e
	}
	if e = audit(tx, room, id, actor, "recording_delete"); e != nil {
		return e
	}
	return tx.Commit()
}

func markRecordingDeleting(tx *sql.Tx, room, id string) error {
	if _, e := tx.Exec("UPDATE recording_runs SET state='deleting',stop_reason='recording_deleted',ended=unixepoch() WHERE id=? AND room_id=? AND state!='deleted'", id, room); e != nil {
		return e
	}
	_, e := tx.Exec("UPDATE processing_jobs SET cancel_requested=1,status=CASE WHEN status='queued' THEN 'cancelled' ELSE 'cancelling' END WHERE run_id=? AND status IN ('queued','transcribing','generating')", id)
	return e
}
