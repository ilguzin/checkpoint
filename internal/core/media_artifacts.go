package core

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ilguzin/checkpoint/internal/minutes"
)

func mediaArtifactName(name string) bool {
	if name == "mix.m4a" || name == "manifest.json" {
		return true
	}
	if !strings.HasPrefix(name, "source-") || !strings.HasSuffix(name, ".webm") || len(name) != 16 {
		return false
	}
	for _, c := range name[7:11] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func artifactRelative(name string) string {
	if strings.HasPrefix(name, "source-") {
		return filepath.Join("sources", name)
	}
	return name
}
func (s *Server) artifactLease(kind, run, lease string) (minutes.Config, error) {
	r, c, e := scanRun(s.store.db.QueryRow(runSelect+" WHERE id=?", run))
	if e != nil {
		return c, e
	}
	var valid bool
	if kind == "recorder" {
		e = s.store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM recording_runs r JOIN rooms rm ON rm.id=r.room_id WHERE r.id=? AND r.recorder_lease=? AND r.recorder_until>=unixepoch() AND r.state IN ('starting','recording','finalizing') AND rm.deletion_state='')", run, lease).Scan(&valid)
	} else {
		e = s.store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM processing_jobs j JOIN recording_runs r ON r.id=j.run_id JOIN rooms rm ON rm.id=r.room_id WHERE j.run_id=? AND j.lease=? AND j.lease_until>=unixepoch() AND j.cancel_requested=0 AND j.status IN ('transcribing','generating') AND rm.deletion_state='')", run, lease).Scan(&valid)
	}
	if e != nil || !valid || r.ID == "" {
		return c, ErrAccess
	}
	return c, nil
}
func (s *Server) mediaArtifactRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /artifacts/{run}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Worker-Kind") != "recorder" || !mediaArtifactName(r.PathValue("name")) {
			fail(w, 403, "Unavailable artifact")
			return
		}
		c, e := s.artifactLease("recorder", r.PathValue("run"), r.Header.Get("X-Task-Lease"))
		if e != nil {
			fail(w, 403, "Lease unavailable")
			return
		}
		path := filepath.Join(c.Storage, r.PathValue("run"), artifactRelative(r.PathValue("name")))
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			fail(w, 503, "Storage unavailable")
			return
		}
		f, e := os.CreateTemp(filepath.Dir(path), ".upload-")
		if e != nil {
			fail(w, 503, "Storage unavailable")
			return
		}
		defer os.Remove(f.Name())
		defer f.Close()
		limit := min(c.MaxStorageBytes, int64(2<<30))
		if r.PathValue("name") == "manifest.json" {
			limit = 2 << 20
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		_, e = io.Copy(f, r.Body)
		if e == nil {
			e = f.Sync()
		}
		if e != nil {
			fail(w, 413, "Artifact could not be saved")
			return
		}
		_ = f.Close()
		if _, e = s.artifactLease("recorder", r.PathValue("run"), r.Header.Get("X-Task-Lease")); e != nil {
			fail(w, 409, "Lease expired")
			return
		}
		if e = os.Rename(f.Name(), path); e != nil {
			fail(w, 503, "Storage unavailable")
			return
		}
		dir, e := os.Open(filepath.Dir(path))
		if e == nil {
			e = dir.Sync()
			dir.Close()
		}
		if e != nil {
			fail(w, 503, "Storage unavailable")
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /artifacts/{run}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Worker-Kind") != "processor" || !mediaArtifactName(r.PathValue("name")) {
			fail(w, 403, "Unavailable artifact")
			return
		}
		c, e := s.artifactLease("processor", r.PathValue("run"), r.Header.Get("X-Task-Lease"))
		if e != nil {
			fail(w, 403, "Lease unavailable")
			return
		}
		root, e := os.OpenRoot(filepath.Join(c.Storage, r.PathValue("run")))
		if e != nil {
			fail(w, 404, "Artifact unavailable")
			return
		}
		defer root.Close()
		f, e := root.Open(artifactRelative(r.PathValue("name")))
		if e != nil {
			fail(w, 404, "Artifact unavailable")
			return
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() {
			fail(w, 404, "Artifact unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, r.PathValue("name"), st.ModTime(), f)
	})
}
