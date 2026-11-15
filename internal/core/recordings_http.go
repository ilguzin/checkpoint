package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ilguzin/checkpoint/internal/minutes"
	"golang.org/x/sys/unix"
)

func (s *Server) minutesConfig() (minutes.Config, error) {
	if s.cfg.MinutesConfig == "" {
		c := minutes.DefaultConfig()
		c.Storage = filepath.Join(filepath.Dir(s.store.path), "recordings")
		return c, nil
	}
	if _, e := os.Stat(s.cfg.MinutesConfig); os.IsNotExist(e) {
		c := minutes.DefaultConfig()
		c.Storage = filepath.Join(filepath.Dir(s.store.path), "recordings")
		return c, nil
	}
	return minutes.LoadConfig(s.cfg.MinutesConfig)
}
func (s *Server) recordingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/media-health", s.mediaHealth)
	mux.HandleFunc("GET /api/recording", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		if !p.Admitted {
			fail(w, 403, "Admission required")
			return
		}
		c, e := s.minutesConfig()
		if e != nil {
			fail(w, 503, "Recording configuration unavailable")
			return
		}
		run, runConfig, e := scanRun(s.store.db.QueryRow(runSelect+" WHERE room_id=? AND state IN ('starting','recording','finalizing')", p.Room))
		var current *minutes.Run
		if e == nil {
			var applied int
			if s.store.db.QueryRow("SELECT applied_window FROM recording_runs WHERE id=?", run.ID).Scan(&applied) == nil && applied != run.Window {
				run.PendingExtension = true
				run.Deadline -= int64(runConfig.ConfirmationSeconds)
			}
			current = &run
		}
		var ready, processorReady bool
		_ = s.store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM media_workers WHERE kind='recorder' AND ready=1 AND seen>unixepoch()-15)").Scan(&ready)
		_ = s.store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM media_workers WHERE kind='processor' AND ready=1 AND seen>unixepoch()-15)").Scan(&processorReady)
		reply(w, 200, map[string]any{"enabled": c.Enabled, "ready": ready, "minutesReady": c.Enabled && processorReady && c.CheckModels() == nil, "run": current, "serverTime": time.Now().Unix(), "externalProcessing": c.Transcription.Adapter == "api" || c.Generator.Adapter == "api"})
	}))
	mux.HandleFunc("POST /api/recording/start", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		var in struct {
			Key     string
			Minutes bool
		}
		if !decode(w, r, &in) {
			return
		}
		c, e := s.minutesConfig()
		if e == nil {
			e = recordingSpace(c)
		}
		if e != nil {
			fail(w, 503, "Recording storage or configuration unavailable; contact the operator")
			return
		}
		out, e := s.store.startRecording(p.Identity, in.Key, in.Minutes, c)
		if e != nil {
			fail(w, 409, "Cannot start recording: check recorder availability, processing setup or an existing recording")
			return
		}
		reply(w, 201, out)
	}))
	mux.HandleFunc("GET /api/rooms/{room}/history", func(w http.ResponseWriter, r *http.Request) {
		_, manage, owner, ok := s.archiveAccess(w, r)
		if !ok {
			return
		}
		out, e := s.store.recordingHistory(r.PathValue("room"))
		if e != nil {
			fail(w, 503, "History unavailable")
			return
		}
		reply(w, 200, map[string]any{"recordings": out, "canManage": manage, "canDelete": owner})
	})
	mux.HandleFunc("GET /api/rooms/{room}/recordings/{run}", func(w http.ResponseWriter, r *http.Request) {
		if _, _, _, ok := s.archiveAccess(w, r); !ok {
			return
		}
		out, e := s.store.recordingDetail(r.PathValue("room"), r.PathValue("run"))
		if e != nil {
			fail(w, 404, "Recording unavailable")
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /api/rooms/{room}/recordings/{run}/{action}", func(w http.ResponseWriter, r *http.Request) {
		s.permissions.Lock()
		defer s.permissions.Unlock()
		actor, manage, _, ok := s.archiveAccess(w, r)
		if !ok {
			return
		}
		if !manage {
			fail(w, 403, "Owner or room admin required")
			return
		}
		var in struct {
			Window    int
			Confirmed bool
			Body      string
			Reviewed  bool
			Base      string
		}
		r.Body = http.MaxBytesReader(w, r.Body, 140<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
			fail(w, 400, "Invalid request")
			return
		}
		var e error
		if r.PathValue("action") == "delete" {
			e = s.store.deleteRecording(r.PathValue("room"), r.PathValue("run"), actor, in.Confirmed)
		} else if r.PathValue("action") == "edit" {
			e = s.store.editMinutes(r.PathValue("room"), r.PathValue("run"), actor, in.Body, in.Reviewed, in.Base)
		} else {
			e = s.store.RecordingControl(r.PathValue("room"), r.PathValue("run"), actor, r.PathValue("action"), in.Window, in.Confirmed)
		}
		if e != nil {
			fail(w, 409, "Action unavailable: refresh the recording status and try again")
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/rooms/{room}/recordings/{run}/audio", func(w http.ResponseWriter, r *http.Request) {
		if _, _, _, ok := s.archiveAccess(w, r); !ok {
			return
		}
		run, c, e := scanRun(s.store.db.QueryRow(runSelect+" WHERE room_id=? AND id=?", r.PathValue("room"), r.PathValue("run")))
		if e != nil || run.MixBytes == 0 || (run.State != "saved" && run.State != "partial") {
			fail(w, 404, "Recording unavailable")
			return
		}
		// A fixed server-generated filename, never a path supplied by a caller.
		root, e := os.OpenRoot(filepath.Join(c.Storage, run.ID))
		if e != nil {
			fail(w, 404, "Recording unavailable")
			return
		}
		defer root.Close()
		f, e := root.Open("mix.m4a")
		if e != nil {
			fail(w, 404, "Recording unavailable")
			return
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() {
			fail(w, 404, "Recording unavailable")
			return
		}
		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("Content-Disposition", `inline; filename="checkpoint-recording.m4a"`)
		http.ServeContent(w, r, "mix.m4a", st.ModTime(), f)
	})
	mux.HandleFunc("POST /api/rooms/{room}/delete", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		var in struct{ Confirmed bool }
		if !decode(w, r, &in) {
			return
		}
		c, e := s.minutesConfig()
		if e != nil {
			fail(w, 503, "Storage unavailable")
			return
		}
		s.permissions.Lock()
		defer s.permissions.Unlock()
		if e = s.store.DeleteRoom(u.ID, r.PathValue("room"), in.Confirmed, c.Storage); e != nil {
			accessError(w, e)
			return
		}
		s.wakeReconcile()
		reply(w, 202, map[string]string{"status": "deleting"})
	}))
}
func (s *Server) archiveAccess(w http.ResponseWriter, r *http.Request) (string, bool, bool, bool) {
	tx, e := s.store.db.Begin()
	if e != nil {
		fail(w, 503, "Archive unavailable")
		return "", false, false, false
	}
	defer tx.Rollback()
	actor, manage, owner, e := s.archiveActor(r, tx, r.PathValue("room"))
	if e != nil {
		fail(w, 403, "Archive access unavailable")
		return "", false, false, false
	}
	return actor, manage, owner, true
}
func recordingSpace(c minutes.Config) error {
	if !c.Enabled {
		return errors.New("disabled")
	}
	if e := os.MkdirAll(c.Storage, 0700); e != nil {
		return e
	}
	var fs unix.Statfs_t
	if e := unix.Statfs(c.Storage, &fs); e != nil {
		return e
	}
	if fs.Bavail*uint64(fs.Bsize) < c.MinFreeBytes {
		return errors.New("storage reserve reached")
	}
	var total int64
	e := filepath.WalkDir(c.Storage, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type().IsRegular() {
			st, e := d.Info()
			if e != nil {
				return e
			}
			total += st.Size()
			if total >= c.MaxStorageBytes {
				return errors.New("recording quota reached")
			}
		}
		return nil
	})
	return e
}
