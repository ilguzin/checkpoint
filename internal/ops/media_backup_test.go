package ops

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ilguzin/checkpoint/internal/core"
	"github.com/ilguzin/checkpoint/internal/minutes"
)

func TestMediaBackupExcludesSourcesAndRequiresCurrentDeletionLedger(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "core.db")
	s, e := core.OpenStore(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	db, e := sql.Open("sqlite", dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	storage := filepath.Join(dir, "recordings")
	id := strings.Repeat("a", 64)
	runDir := filepath.Join(storage, id)
	if e = os.MkdirAll(filepath.Join(runDir, "sources"), 0700); e != nil {
		t.Fatal(e)
	}
	body := []byte("verified test media")
	sum := sha256.Sum256(body)
	m := minutes.Manifest{Run: id, Started: 1, Ended: 10, MixBytes: int64(len(body)), MixHash: hex.EncodeToString(sum[:]), Sources: []minutes.Source{}}
	for name, data := range map[string][]byte{"mix.m4a": body, "sources/source-0000.webm": []byte("must not be backed up")} {
		if e = os.WriteFile(filepath.Join(runDir, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e = minutes.AtomicJSON(filepath.Join(runDir, "manifest.json"), m); e != nil {
		t.Fatal(e)
	}
	cfg := minutes.DefaultConfig()
	cfg.Storage = storage
	profile, _ := json.Marshal(cfg)
	manifest, _ := json.Marshal(m)
	if _, e = db.Exec("INSERT INTO rooms(id,name,mode) VALUES('room','History','meeting'); INSERT INTO meeting_occurrences VALUES('occurrence','room',0,1,10)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO recording_runs(id,room_id,occurrence_id,request_key,requested_by,minutes_requested,profile,manifest,state,started,ended,mix_bytes) VALUES(?,'room','occurrence','request','actor',0,?,?,'saved',1,10,?)", id, string(profile), string(manifest), len(body)); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(dir, "backup.tar.gz")
	if e = Backup(dbPath, archive, ""); e != nil {
		t.Fatal(e)
	}
	file, e := os.Open(archive)
	if e != nil {
		t.Fatal(e)
	}
	gz, e := gzip.NewReader(file)
	if e != nil {
		t.Fatal(e)
	}
	tarReader := tar.NewReader(gz)
	found := false
	for {
		h, e := tarReader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(h.Name, "sources/") {
			t.Fatal("individual tracks leaked into backup")
		}
		if strings.HasSuffix(h.Name, "/mix.m4a") {
			found = true
		}
	}
	gz.Close()
	file.Close()
	if !found {
		t.Fatal("historical mix missing")
	}
	destination := filepath.Join(dir, "restored.db")
	if e = Restore(archive, destination, ""); e == nil {
		t.Fatal("media restored without current deletion ledger")
	}
	ledger := filepath.Join(storage, "deletions")
	if e = os.Mkdir(ledger, 0700); e != nil {
		t.Fatal(e)
	}
	if e = RestoreWithMedia(archive, destination, "", ledger); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(destination+".recordings", id, "mix.m4a")); e != nil || string(b) != string(body) {
		t.Fatal("restored mix differs", e)
	}
}
