package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ilguzin/checkpoint/internal/minutes"
	"github.com/livekit/protocol/auth"
)

func (s *Server) MediaHandler(recorderToken, processorToken string) http.Handler {
	mux := http.NewServeMux()
	s.mediaArtifactRoutes(mux)
	mux.HandleFunc("POST /discard", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Run string }
		if !decode(w, r, &in) {
			return
		}
		var state string
		e := s.store.db.QueryRow("SELECT state FROM recording_runs WHERE id=?", in.Run).Scan(&state)
		discard := errors.Is(e, sql.ErrNoRows) || state == "saved" || state == "partial" || state == "failed" || state == "deleting" || state == "deleted"
		reply(w, 200, map[string]bool{"discard": discard})
	})
	mux.HandleFunc("POST /configuration", func(w http.ResponseWriter, r *http.Request) {
		c, e := s.minutesConfig()
		if e != nil {
			fail(w, 503, "Configuration unavailable")
			return
		}
		reply(w, 200, c)
	})
	mux.HandleFunc("POST /claim", func(w http.ResponseWriter, r *http.Request) {
		kind := r.Header.Get("X-Worker-Kind")
		var in struct{ Ready bool }
		if !decode(w, r, &in) {
			return
		}
		out, e := s.claimMedia(kind, in.Ready)
		if e != nil {
			fail(w, 503, "Queue unavailable")
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var in minutes.Heartbeat
		if !decode(w, r, &in) {
			return
		}
		out, e := s.mediaHeartbeat(r.Header.Get("X-Worker-Kind"), in)
		if e != nil {
			fail(w, 409, "Lease unavailable")
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		var in minutes.Result
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
			fail(w, 400, "Invalid result")
			return
		}
		if e := s.mediaResult(r.Header.Get("X-Worker-Kind"), in); e != nil {
			fail(w, 409, "Result not accepted")
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := ""
		switch r.Header.Get("X-Worker-Kind") {
		case "recorder":
			want = recorderToken
		case "processor":
			want = processorToken
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(want) < 32 || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			fail(w, 401, "Worker authentication required")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) claimMedia(kind string, ready bool) (*minutes.Task, error) {
	c, e := s.minutesConfig()
	if e != nil {
		return nil, e
	}
	tx, e := s.store.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("INSERT INTO media_workers(kind,seen,ready) VALUES(?,unixepoch(),?) ON CONFLICT(kind) DO UPDATE SET seen=excluded.seen,ready=excluded.ready", kind, ready); e != nil {
		return nil, e
	}
	task := &minutes.Task{Kind: kind}
	var profile string
	if kind == "recorder" {
		// An expired recorder is never silently resumed. It is disconnected and its
		// durable files are salvaged by a new lease, without joining the media room.
		row := tx.QueryRow(`SELECT r.id,r.room_id,r.occurrence_id,r.state,r.processing,r.minutes_requested,r.started,r.ended,r.deadline,r.window,r.stop_reason,r.mix_bytes,r.partial,r.profile FROM recording_runs r JOIN rooms rm ON rm.id=r.room_id WHERE r.state IN ('starting','recording','finalizing') AND r.recorder_until<unixepoch() AND rm.deletion_state='' ORDER BY r.created LIMIT 1`)
		task.Run, task.Config, e = scanRun(row)
		if errors.Is(e, sql.ErrNoRows) || (!c.Enabled && task.Run.State == "starting") {
			return nil, tx.Commit()
		}
		if e != nil {
			return nil, e
		}
		var previousLease string
		if e = tx.QueryRow("SELECT recorder_lease FROM recording_runs WHERE id=?", task.Run.ID).Scan(&previousLease); e != nil {
			return nil, e
		}
		if task.Run.State == "recording" || (task.Run.State == "starting" && previousLease != "") {
			task.Run.State = "finalizing"
			task.Run.StopReason = "recorder_interrupted"
			task.Run.Partial = true
			task.Run.Ended = time.Now().Unix()
			if _, e = tx.Exec("UPDATE recording_runs SET state='finalizing',stop_reason='recorder_interrupted',partial=1,ended=unixepoch() WHERE id=?", task.Run.ID); e != nil {
				return nil, e
			}
		}
		if !ready && task.Run.State == "starting" {
			return nil, tx.Commit()
		}
		var gen int
		if e = tx.QueryRow("SELECT generation FROM meeting_occurrences WHERE id=?", task.Run.Occurrence).Scan(&gen); e != nil {
			return nil, e
		}
		task.MediaRoom = task.Run.Room
		if gen > 0 {
			task.MediaRoom += "--" + fmtInt(gen)
		}
		task.Lease = randomID()
		if _, e = tx.Exec("UPDATE recording_runs SET recorder_lease=?,recorder_until=unixepoch()+30 WHERE id=?", task.Lease, task.Run.ID); e != nil {
			return nil, e
		}
		task.URL = s.cfg.LiveKitURL
		task.Token, e = auth.NewAccessToken(s.cfg.APIKey, s.cfg.APISecret).SetIdentity("rec-" + task.Run.ID + "-" + task.Lease).SetName("Checkpoint recording").SetValidFor(3 * time.Hour).SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: task.MediaRoom, CanPublish: boolPtr(false), CanSubscribe: boolPtr(true), CanPublishData: boolPtr(false), Hidden: true, Recorder: true}).ToJWT()
		if e != nil {
			return nil, e
		}
	} else {
		// A single active heavy job globally; leases survive worker restarts.
		var active bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM processing_jobs WHERE status IN ('transcribing','generating','cancelling') AND lease_until>=unixepoch())").Scan(&active); e != nil {
			return nil, e
		}
		if active || !ready || !c.Enabled {
			return nil, tx.Commit()
		}
		var id string
		e = tx.QueryRow("SELECT j.id,j.run_id FROM processing_jobs j JOIN recording_runs r ON r.id=j.run_id JOIN rooms rm ON rm.id=r.room_id WHERE j.status='queued' AND j.cancel_requested=0 AND j.available_at<=unixepoch() AND r.state IN ('saved','partial') AND rm.deletion_state='' ORDER BY j.available_at,j.rowid LIMIT 1").Scan(&id, &task.Run.ID)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, tx.Commit()
		}
		if e != nil {
			return nil, e
		}
		task.Run, task.Config, e = scanRun(tx.QueryRow(runSelect+" WHERE id=?", task.Run.ID))
		if e != nil {
			return nil, e
		}
		if e = tx.QueryRow("SELECT manifest FROM recording_runs WHERE id=?", task.Run.ID).Scan(&profile); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(profile), &task.Manifest); e != nil {
			return nil, e
		}
		var body string
		e = tx.QueryRow("SELECT body FROM recording_transcripts WHERE run_id=?", task.Run.ID).Scan(&body)
		if e == nil {
			if e = json.Unmarshal([]byte(body), &task.Transcript); e != nil {
				return nil, e
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		task.Lease = randomID()
		phase := "transcribing"
		if task.Transcript != nil {
			phase = "generating"
		}
		if _, e = tx.Exec("UPDATE processing_jobs SET status=?,lease=?,lease_until=unixepoch()+30,attempts=attempts+1,started=unixepoch() WHERE id=?", phase, task.Lease, id); e != nil {
			return nil, e
		}
		if _, e = tx.Exec("UPDATE recording_runs SET processing=? WHERE id=?", phase, task.Run.ID); e != nil {
			return nil, e
		}
	}
	if e = tx.QueryRow("SELECT name FROM rooms WHERE id=?", task.Run.Room).Scan(&task.Title); e != nil {
		return nil, e
	}
	return task, tx.Commit()
}
func fmtInt(n int) string { b, _ := json.Marshal(n); return string(b) }
func (s *Server) mediaHeartbeat(kind string, in minutes.Heartbeat) (minutes.Directive, error) {
	out := minutes.Directive{}
	tx, e := s.store.db.Begin()
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	r, c, e := scanRun(tx.QueryRow(runSelect+" WHERE id=?", in.Run))
	if e != nil {
		return out, e
	}
	var deleting bool
	if e = tx.QueryRow("SELECT deletion_state!='' FROM rooms WHERE id=?", r.Room).Scan(&deleting); e != nil {
		return out, e
	}
	if kind == "recorder" {
		var lease string
		var until int64
		if e = tx.QueryRow("SELECT recorder_lease,recorder_until FROM recording_runs WHERE id=?", in.Run).Scan(&lease, &until); e != nil || lease != in.Lease || until < time.Now().Unix() {
			return out, ErrAccess
		}
		if in.Phase == "recording" && r.State == "starting" {
			if in.Started <= 0 || in.Started > time.Now().Unix()+5 || in.Started < time.Now().Unix()-30 {
				return out, errors.New("invalid start time")
			}
			r.State = "recording"
			r.Started = in.Started
			r.Deadline = r.Started + int64(c.ConfirmationSeconds)
			if _, e = tx.Exec("UPDATE recording_runs SET state='recording',started=?,deadline=? WHERE id=?", r.Started, r.Deadline, r.ID); e != nil {
				return out, e
			}
		}
		if in.Reason != "" && r.State == "recording" {
			reason := "recorder_interrupted"
			switch in.Reason {
			case "confirmation_timeout", "storage_limit", "max_duration", "shutdown", "source_limit":
				reason = in.Reason
			}
			if e = stopRecording(tx, r.ID, reason); e != nil {
				return out, e
			}
			r.State = "finalizing"
		}
		out.Stop = deleting || r.State == "finalizing" || r.State == "failed" || r.State == "deleting" || r.State == "deleted"
		out.Deadline = r.Deadline
		out.Window = r.Window
		if _, e = tx.Exec("UPDATE recording_runs SET recorder_until=unixepoch()+30,applied_window=CASE WHEN window=? THEN window ELSE applied_window END WHERE id=?", in.AppliedWindow, r.ID); e != nil {
			return out, e
		}
		out.Sources = map[string]minutes.Source{}
		rows, e := tx.Query(sessionSelect+" AND s.room_id=? AND s.admitted=1", r.Room)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			p, e := scanSession(rows)
			if e != nil {
				rows.Close()
				return out, e
			}
			out.Sources[p.Identity] = minutes.Source{Identity: p.Identity, UserID: p.UserID, Name: p.Name}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
	} else {
		var cancelled bool
		var started int64
		var status string
		e = tx.QueryRow("SELECT cancel_requested,started,status FROM processing_jobs WHERE run_id=? AND lease=? AND lease_until>=unixepoch() AND status IN ('transcribing','generating','cancelling')", r.ID, in.Lease).Scan(&cancelled, &started, &status)
		if e != nil {
			return out, e
		}
		out.Cancel = deleting || cancelled || time.Now().Unix()-started > int64(c.JobTimeoutSeconds)
		if _, e = tx.Exec("UPDATE processing_jobs SET lease_until=unixepoch()+30 WHERE run_id=? AND lease=?", r.ID, in.Lease); e != nil {
			return out, e
		}
	}
	_, e = tx.Exec("UPDATE media_workers SET seen=unixepoch() WHERE kind=?", kind)
	if e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (s *Server) mediaResult(kind string, in minutes.Result) error {
	// Hashing large audio files must not hold the SQLite write transaction.
	if kind == "recorder" && in.Error == "" && in.Manifest != nil {
		_, profile, err := scanRun(s.store.db.QueryRow(runSelect+" WHERE id=?", in.Run))
		if err != nil {
			return err
		}
		if err = verifyMix(profile.Storage, in.Run, *in.Manifest); err != nil {
			return err
		}
		if err = verifySources(profile.Storage, in.Run, *in.Manifest); err != nil {
			return err
		}
	}

	tx, e := s.store.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, c, e := scanRun(tx.QueryRow(runSelect+" WHERE id=?", in.Run))
	if e != nil {
		return e
	}
	var deleting bool
	if e = tx.QueryRow("SELECT deletion_state!='' FROM rooms WHERE id=?", r.Room).Scan(&deleting); e != nil || deleting {
		return ErrAccess
	}
	if kind == "recorder" {
		var valid bool
		if e = tx.QueryRow("SELECT recorder_lease=? AND recorder_until>=unixepoch() AND state IN ('starting','recording','finalizing') FROM recording_runs WHERE id=?", in.Lease, r.ID).Scan(&valid); e != nil || !valid {
			return ErrAccess
		}
		if in.Error != "" {
			_, e = tx.Exec("UPDATE recording_runs SET state='failed',ended=unixepoch(),recorder_until=0,recorder_lease='',stop_reason='recorder_failed',partial=1 WHERE id=?", r.ID)
			if e != nil {
				return e
			}
			return tx.Commit()
		}
		m := in.Manifest
		if m == nil || m.Run != r.ID || m.MixBytes <= 0 || len(m.MixHash) != 64 || len(m.Sources) > c.MaxSources*8 {
			return errors.New("invalid manifest")
		}
		// Artifacts were verified before opening this transaction; the lease is rechecked above.
		body, _ := json.Marshal(m)
		state := "saved"
		if m.Partial || r.Partial {
			state = "partial"
		}
		if _, e = tx.Exec("UPDATE recording_runs SET state=?,started=?,ended=?,manifest=?,mix_bytes=?,partial=?,recorder_lease='',recorder_until=0 WHERE id=?", state, m.Started, m.Ended, string(body), m.MixBytes, state == "partial", r.ID); e != nil {
			return e
		}
		if r.MinutesRequested {
			if e = queueMinutes(tx, r.ID); e != nil {
				return e
			}
		}
	} else {
		var id, status string
		var cancelled bool
		var attempts int
		e = tx.QueryRow("SELECT id,status,cancel_requested,attempts FROM processing_jobs WHERE run_id=? AND lease=? AND lease_until>=unixepoch() AND status IN ('transcribing','generating','cancelling')", r.ID, in.Lease).Scan(&id, &status, &cancelled, &attempts)
		if e != nil {
			return ErrAccess
		}
		next := ""
		terminal := true
		switch {
		case cancelled || in.Phase == "cancelled":
			next = "cancelled"
		case in.Error != "":
			next = "failed"
			if attempts < c.MaxAttempts {
				next = "queued"
			}
		case in.Phase == "transcript":
			if status != "transcribing" {
				return ErrAccess
			}
			if e = minutes.ValidateTranscript(in.Transcript, (r.Ended-r.Started)*1000); e != nil {
				return e
			}
			b, _ := json.Marshal(in.Transcript)
			if _, e = tx.Exec("INSERT INTO recording_transcripts(run_id,body) VALUES(?,?) ON CONFLICT(run_id) DO NOTHING", r.ID, string(b)); e != nil {
				return e
			}
			next = "generating"
			terminal = false
			if len(in.Transcript) == 0 {
				next = "no_speech"
				terminal = true
			}
		case in.Phase == "ready":
			if status != "generating" || len(in.Markdown) == 0 || len(in.Markdown) > 128<<10 {
				return errors.New("invalid minutes")
			}
			if _, e = tx.Exec("INSERT INTO minutes_revisions(id,run_id,job_id,body,author) VALUES(?,?,?,?,?)", randomID(), r.ID, id, in.Markdown, "ai"); e != nil {
				return e
			}
			next = "ready"
		default:
			return errors.New("invalid result phase")
		}
		if _, e = tx.Exec("UPDATE recording_runs SET processing=? WHERE id=?", next, r.ID); e != nil {
			return e
		}
		if terminal {
			_, e = tx.Exec("UPDATE processing_jobs SET status=?,lease='',lease_until=0,last_error=?,available_at=unixepoch()+? WHERE id=?", next, safeJobError(in.Error), attempts*30, id)
		} else {
			_, e = tx.Exec("UPDATE processing_jobs SET status=?,lease_until=unixepoch()+30 WHERE id=?", next, id)
		}
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
func safeJobError(s string) string {
	if s == "" {
		return ""
	}
	return "processing_failed"
}
func verifyMix(storage, id string, m minutes.Manifest) error {
	root, e := os.OpenRoot(filepath.Join(storage, id))
	if e != nil {
		return e
	}
	defer root.Close()
	f, e := root.Open("mix.m4a")
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() != m.MixBytes {
		return errors.New("mix size mismatch")
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return e
	}
	if hex.EncodeToString(h.Sum(nil)) != m.MixHash {
		return errors.New("mix hash mismatch")
	}
	return nil
}
func (s *Server) isRecorder(identity, room string) bool {
	if !strings.HasPrefix(identity, "rec-") {
		return false
	}
	parts := strings.Split(identity, "-")
	if len(parts) != 3 {
		return false
	}
	var ok bool
	e := s.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM recording_runs r JOIN meeting_occurrences o ON o.id=r.occurrence_id JOIN rooms rm ON rm.id=r.room_id WHERE r.id=? AND r.recorder_lease=? AND r.recorder_until>=unixepoch() AND r.state IN ('starting','recording','finalizing') AND rm.deletion_state='' AND (CASE WHEN o.generation=0 THEN r.room_id ELSE r.room_id||'--'||o.generation END)=?)`, parts[1], parts[2], room).Scan(&ok)
	return e == nil && ok
}
func (s *Server) recordingPresence(mediaRoom string, connected map[string]bool) {
	// Only authoritative, admitted human sessions count. Unknown media-server state
	// never calls this function, so it cannot start an empty-room timeout.
	humans := 0
	for id := range connected {
		if p, ok := s.store.Get(id); ok && p.Admitted {
			humans++
		}
	}
	tx, e := s.store.db.Begin()
	if e != nil {
		return
	}
	defer tx.Rollback()
	rows, e := tx.Query(`SELECT r.id,r.profile,r.empty_since FROM recording_runs r JOIN meeting_occurrences o ON o.id=r.occurrence_id WHERE (CASE WHEN o.generation=0 THEN r.room_id ELSE r.room_id||'--'||o.generation END)=? AND r.state IN ('starting','recording')`, mediaRoom)
	if e != nil {
		return
	}
	type v struct {
		id, profile string
		empty       int64
	}
	var runs []v
	for rows.Next() {
		var a v
		if rows.Scan(&a.id, &a.profile, &a.empty) != nil {
			rows.Close()
			return
		}
		runs = append(runs, a)
	}
	rows.Close()
	for _, r := range runs {
		var c minutes.Config
		if json.Unmarshal([]byte(r.profile), &c) != nil {
			continue
		}
		if humans > 0 {
			_, e = tx.Exec("UPDATE recording_runs SET empty_since=0 WHERE id=?", r.id)
		} else if r.empty == 0 {
			_, e = tx.Exec("UPDATE recording_runs SET empty_since=unixepoch() WHERE id=?", r.id)
		} else if time.Now().Unix()-r.empty >= int64(c.EmptySeconds) {
			e = stopRecording(tx, r.id, "empty_room")
			if e == nil {
				_, e = tx.Exec("UPDATE meeting_occurrences SET ended=unixepoch() WHERE id=(SELECT occurrence_id FROM recording_runs WHERE id=?)", r.id)
			}
		}
		if e != nil {
			return
		}
	}
	_ = tx.Commit()
}

func (s *Server) MaintainMedia(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		s.maintainMediaOnce()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) maintainMediaOnce() {
	c, e := s.minutesConfig()
	if e != nil {
		return
	}
	tx, e := s.store.db.Begin()
	if e != nil {
		return
	}
	// Normal stops all share the finalization path.
	_, e = tx.Exec(`UPDATE recording_runs SET state='finalizing',ended=unixepoch(),stop_reason=CASE WHEN deadline>0 AND deadline<=unixepoch() THEN 'confirmation_timeout' ELSE 'meeting_ended' END WHERE state IN ('starting','recording') AND ((deadline>0 AND deadline<=unixepoch()) OR EXISTS(SELECT 1 FROM rooms r JOIN meeting_occurrences o ON o.id=recording_runs.occurrence_id WHERE r.id=recording_runs.room_id AND (r.closed=1 OR r.generation!=o.generation)))`)
	if e != nil {
		tx.Rollback()
		return
	}
	rows, e := tx.Query(`SELECT j.id,j.run_id,j.cancel_requested,j.attempts,r.profile FROM processing_jobs j JOIN recording_runs r ON r.id=j.run_id WHERE j.status IN ('transcribing','generating','cancelling') AND j.lease_until<unixepoch()`)
	if e != nil {
		tx.Rollback()
		return
	}
	type expired struct {
		id, run, profile string
		cancel           bool
		attempts         int
	}
	list := []expired{}
	for rows.Next() {
		var x expired
		if rows.Scan(&x.id, &x.run, &x.cancel, &x.attempts, &x.profile) != nil {
			rows.Close()
			tx.Rollback()
			return
		}
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		var p minutes.Config
		_ = json.Unmarshal([]byte(x.profile), &p)
		status := "queued"
		if x.cancel {
			status = "cancelled"
		} else if x.attempts >= p.MaxAttempts {
			status = "failed"
		}
		if _, e = tx.Exec("UPDATE processing_jobs SET status=?,lease='',lease_until=0,available_at=unixepoch()+30 WHERE id=?", status, x.id); e != nil {
			tx.Rollback()
			return
		}
		if _, e = tx.Exec("UPDATE recording_runs SET processing=? WHERE id=?", status, x.run); e != nil {
			tx.Rollback()
			return
		}
	}
	if tx.Commit() != nil {
		return
	}
	s.cleanupMedia(c)
}

// PrepareMedia applies the durable deletion ledger before any public request can
// observe restored data. It is required even when recording is disabled.
func (s *Server) PrepareMedia() error {
	// A malformed optional profile must not stop calls. Existing run snapshots
	// still locate their deletion ledgers and must be reconciled before serving.
	paths := map[string]bool{filepath.Join(filepath.Dir(s.store.path), "recordings"): true}
	if c, err := s.minutesConfig(); err == nil {
		paths[c.Storage] = true
	}
	rows, err := s.store.db.Query("SELECT DISTINCT profile FROM recording_runs")
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var profile minutes.Config
		if err = json.Unmarshal([]byte(raw), &profile); err != nil {
			rows.Close()
			return err
		}
		if profile.Storage != "" {
			paths[profile.Storage] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for path := range paths {
		if err = s.store.restoreDeletionLedger(path); err != nil {
			return err
		}
	}
	return nil
}

// Source checksums are optional for pre-upgrade manifests; current recorders emit
// them for every retained audio source. Workers never have direct archive access.
func verifySources(storage, id string, m minutes.Manifest) error {
	root, err := os.OpenRoot(filepath.Join(storage, id))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, src := range m.Sources {
		if src.SHA256 == "" {
			continue
		}
		if filepath.Dir(src.File) != "sources" || !mediaArtifactName(filepath.Base(src.File)) {
			return errors.New("invalid source path")
		}
		f, e := root.Open(src.File)
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, f)
		f.Close()
		if e != nil {
			return e
		}
		if n != src.Bytes || hex.EncodeToString(h.Sum(nil)) != src.SHA256 {
			return errors.New("source checksum mismatch")
		}
	}
	return nil
}
