package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ilguzin/checkpoint/internal/minutes"
)

func (s *Server) cleanupMedia(c minutes.Config) {
	s.purgeRecordings()
	rows, e := s.store.db.Query(`SELECT id,profile,manifest,minutes_requested,ended,EXISTS(SELECT 1 FROM recording_transcripts t WHERE t.run_id=r.id),EXISTS(SELECT 1 FROM processing_jobs j WHERE j.run_id=r.id AND j.status IN ('transcribing','generating','cancelling') AND j.lease_until>=unixepoch()) FROM recording_runs r WHERE state IN ('saved','partial','failed') AND cleanup_done=0`)
	if e != nil {
		return
	}
	type entry struct {
		id, profile, manifest string
		requested             bool
		ended                 int64
		transcribed, active   bool
	}
	var entries []entry
	for rows.Next() {
		var v entry
		if rows.Scan(&v.id, &v.profile, &v.manifest, &v.requested, &v.ended, &v.transcribed, &v.active) != nil {
			rows.Close()
			return
		}
		entries = append(entries, v)
	}
	rows.Close()
	for _, v := range entries {
		var p minutes.Config
		var m minutes.Manifest
		if json.Unmarshal([]byte(v.profile), &p) != nil || json.Unmarshal([]byte(v.manifest), &m) != nil {
			continue
		}
		if v.active && !v.transcribed {
			continue
		}
		expired := v.ended > 0 && time.Now().Unix()-v.ended >= int64(p.FailedAudioTTLSeconds)
		if v.requested && !v.transcribed && !expired {
			continue
		}
		if !expired && verifyMix(p.Storage, v.id, m) != nil {
			continue
		}
		// The sources directory contains only temporary participant media and decoded
		// ASR inputs. Mix, manifests and database transcript/revisions remain intact.
		if os.RemoveAll(filepath.Join(p.Storage, v.id, "sources")) == nil {
			_, _ = s.store.db.Exec("UPDATE recording_runs SET cleanup_done=1 WHERE id=?", v.id)
		}
	}
	rows, e = s.store.db.Query("SELECT room_id FROM room_deletions WHERE completed=0")
	if e != nil {
		return
	}
	var rooms []string
	for rows.Next() {
		var room string
		if rows.Scan(&room) != nil {
			rows.Close()
			return
		}
		rooms = append(rooms, room)
	}
	rows.Close()
	for _, room := range rooms {
		s.purgeRoom(room, c.Storage)
	}
}
func (s *Server) purgeRoom(room, storage string) {
	var active bool
	if s.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM recording_runs WHERE room_id=? AND recorder_until>=unixepoch()) OR EXISTS(SELECT 1 FROM processing_jobs j JOIN recording_runs r ON r.id=j.run_id WHERE r.room_id=? AND j.lease_until>=unixepoch())`, room, room).Scan(&active) != nil || active {
		return
	}
	rows, e := s.store.db.Query("SELECT id,profile FROM recording_runs WHERE room_id=?", room)
	if e != nil {
		return
	}
	type item struct{ id, profile string }
	var runs []item
	for rows.Next() {
		var x item
		if rows.Scan(&x.id, &x.profile) != nil {
			rows.Close()
			return
		}
		runs = append(runs, x)
	}
	rows.Close()
	for _, x := range runs {
		var c minutes.Config
		if json.Unmarshal([]byte(x.profile), &c) != nil {
			return
		}
		if os.RemoveAll(filepath.Join(c.Storage, x.id)) != nil {
			return
		}
	}
	tx, e := s.store.db.Begin()
	if e != nil {
		return
	}
	defer tx.Rollback()
	for _, table := range []string{"minutes_revisions", "recording_transcripts", "processing_jobs"} {
		if _, e = tx.Exec("DELETE FROM "+table+" WHERE run_id IN (SELECT id FROM recording_runs WHERE room_id=?)", room); e != nil {
			return
		}
	}
	for _, table := range []string{"recording_runs", "meeting_occurrences", "messages", "room_memberships", "room_admin_invites", "room_admins", "sessions", "invites", "room_bans", "recording_audit"} {
		if _, e = tx.Exec("DELETE FROM "+table+" WHERE room_id=?", room); e != nil {
			return
		}
	}
	// Keep only the opaque room ID as a permanent non-reusable tombstone.
	if _, e = tx.Exec("UPDATE rooms SET name='',owner_id=NULL,closed=1,deletion_state='deleted',screen_identity='',screen_until=0 WHERE id=?", room); e != nil {
		return
	}
	if _, e = tx.Exec("UPDATE room_deletions SET completed=unixepoch() WHERE room_id=?", room); e != nil {
		return
	}
	_ = tx.Commit()
}

func (s *Server) purgeRecordings() {
	rows, e := s.store.db.Query("SELECT id,profile FROM recording_runs r WHERE state='deleting' AND recorder_until<unixepoch() AND NOT EXISTS(SELECT 1 FROM processing_jobs j WHERE j.run_id=r.id AND j.lease_until>=unixepoch())")
	if e != nil {
		return
	}
	type item struct{ id, profile string }
	var items []item
	for rows.Next() {
		var i item
		if rows.Scan(&i.id, &i.profile) != nil {
			rows.Close()
			return
		}
		items = append(items, i)
	}
	rows.Close()
	for _, i := range items {
		var c minutes.Config
		if json.Unmarshal([]byte(i.profile), &c) != nil {
			continue
		}
		if os.RemoveAll(filepath.Join(c.Storage, i.id)) != nil {
			continue
		}
		tx, e := s.store.db.Begin()
		if e != nil {
			return
		}
		ok := true
		for _, table := range []string{"minutes_revisions", "recording_transcripts", "processing_jobs"} {
			if _, e = tx.Exec("DELETE FROM "+table+" WHERE run_id=?", i.id); e != nil {
				ok = false
				break
			}
		}
		if ok {
			_, e = tx.Exec("UPDATE recording_runs SET state='deleted',manifest='{}',mix_bytes=0,processing='cancelled' WHERE id=?", i.id)
			ok = e == nil
		}
		if ok {
			_ = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
}
