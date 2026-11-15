package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ilguzin/checkpoint/internal/minutes"
)

// MediaBackupFiles is called on the immutable SQLite snapshot, not the live DB.
// Only durable historical artifacts are included; participant audio is transient.
func (s *Store) MediaBackupFiles() (map[string]string, error) {
	rows, e := s.db.Query("SELECT id,profile,manifest FROM recording_runs WHERE state IN ('saved','partial')")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]string{}
	roots := map[string]bool{}
	for rows.Next() {
		var id, profile, body string
		if e = rows.Scan(&id, &profile, &body); e != nil {
			return nil, e
		}
		var c minutes.Config
		var m minutes.Manifest
		if json.Unmarshal([]byte(profile), &c) != nil || json.Unmarshal([]byte(body), &m) != nil {
			return nil, errors.New("invalid recording snapshot")
		}
		if e = verifyMix(c.Storage, id, m); e != nil {
			return nil, e
		}
		out["recordings/"+id+"/mix.m4a"] = filepath.Join(c.Storage, id, "mix.m4a")
		out["recordings/"+id+"/manifest.json"] = filepath.Join(c.Storage, id, "manifest.json")
		roots[c.Storage] = true
	}
	for root := range roots {
		entries, e := os.ReadDir(filepath.Join(root, "deletions"))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				return nil, errors.New("invalid deletion ledger")
			}
			out["recordings/deletions/"+entry.Name()] = filepath.Join(root, "deletions", entry.Name())
		}
	}
	return out, rows.Err()
}
func (s *Store) RebindRecordingStorage(storage string) error {
	rows, e := s.db.Query("SELECT id,profile FROM recording_runs")
	if e != nil {
		return e
	}
	type item struct{ id, profile string }
	var items []item
	for rows.Next() {
		var i item
		if e = rows.Scan(&i.id, &i.profile); e != nil {
			rows.Close()
			return e
		}
		items = append(items, i)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, i := range items {
		var c minutes.Config
		if e = json.Unmarshal([]byte(i.profile), &c); e != nil {
			return e
		}
		c.Storage = storage
		b, _ := json.Marshal(c)
		if _, e = tx.Exec("UPDATE recording_runs SET profile=?,recorder_lease='',recorder_until=0,cleanup_done=1 WHERE id=?", string(b), i.id); e != nil {
			return e
		}
	}
	// Backups do not contain incomplete capture sources. Do not resume a call or
	// pretend an unfinished recording has a playable mix after restore.
	if _, e = tx.Exec("UPDATE recording_runs SET state='failed',partial=1,stop_reason='backup_restore',ended=unixepoch() WHERE state IN ('starting','recording','finalizing')"); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE processing_jobs SET status=CASE WHEN cancel_requested=1 THEN 'cancelled' ELSE 'queued' END,lease='',lease_until=0,available_at=unixepoch() WHERE status IN ('transcribing','generating','cancelling')"); e != nil {
		return e
	}
	return tx.Commit()
}
