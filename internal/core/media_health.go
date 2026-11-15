package core

import (
	"net/http"
	"time"
)

func (s *Server) mediaHealth(w http.ResponseWriter, r *http.Request) {
	c, e := s.minutesConfig()
	if e != nil {
		reply(w, 200, map[string]any{"state": "unavailable", "enabled": false})
		return
	}
	out := map[string]any{"enabled": c.Enabled, "state": "disabled", "recorder": "unavailable", "processor": "unavailable", "queue": 0, "recordings": 0, "oldestSeconds": 0, "checkedAt": time.Now().UTC()}
	if c.Enabled {
		out["state"] = "degraded"
		for _, kind := range []string{"recorder", "processor"} {
			var ready bool
			_ = s.store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM media_workers WHERE kind=? AND ready=1 AND seen>unixepoch()-15)", kind).Scan(&ready)
			if ready {
				out[kind] = "ready"
			}
		}
		if out["recorder"] == "ready" {
			out["state"] = "operational"
		}
		var queue, recordings, oldest int64
		_ = s.store.db.QueryRow("SELECT count(*),COALESCE(max(unixepoch()-available_at),0) FROM processing_jobs WHERE status IN ('queued','transcribing','generating')").Scan(&queue, &oldest)
		_ = s.store.db.QueryRow("SELECT count(*) FROM recording_runs WHERE state IN ('starting','recording','finalizing')").Scan(&recordings)
		out["queue"] = queue
		out["recordings"] = recordings
		out["oldestSeconds"] = max(int64(0), oldest)
	}
	reply(w, 200, out)
}

// MediaWorkPending is used by the native updater before a coordinated switch.
func (s *Store) MediaWorkPending() (bool, error) {
	var busy bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM recording_runs WHERE state IN ('starting','recording','finalizing')) OR EXISTS(SELECT 1 FROM processing_jobs WHERE status IN ('queued','transcribing','generating','cancelling'))").Scan(&busy)
	return busy, err
}
