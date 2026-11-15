package ops

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/ilguzin/checkpoint/internal/core"
)

func TestBackupRestoresSQLiteAndPrivateConfiguration(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "source.db")
	s, e := core.OpenStore(db)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p, secret, e := s.Add("Guest", "room", true, true)
	if e != nil {
		t.Fatal(e)
	}
	configs := filepath.Join(dir, "config")
	os.Mkdir(configs, 0700)
	os.WriteFile(filepath.Join(configs, "core.env"), []byte("PRIVATE=fixture-value\n"), 0600)
	archive := filepath.Join(dir, "backup.tar.gz")
	if e = Backup(db, archive, configs); e != nil {
		t.Fatal(e)
	}
	if info, _ := os.Stat(archive); info.Mode().Perm() != 0600 {
		t.Fatal("backup not private")
	}
	restored := filepath.Join(dir, "restored.db")
	restoredConfigs := filepath.Join(dir, "restored-config")
	if e = Restore(archive, restored, restoredConfigs); e != nil {
		t.Fatal(e)
	}
	reopened, e := core.OpenStore(restored)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	out, ok := reopened.Authenticate(secret)
	if !ok || out.Identity != p.Identity {
		t.Fatal("session not restored")
	}
	body, _ := os.ReadFile(filepath.Join(restoredConfigs, "core.env"))
	if string(body) != "PRIVATE=fixture-value\n" {
		t.Fatal("config missing")
	}
	if e = Restore(archive, restored, ""); e == nil {
		t.Fatal("restore overwrote live destination")
	}
	if e = Backup(db, archive, configs); e == nil {
		t.Fatal("backup overwrote earlier backup")
	}
}
func TestRestoreRejectsTraversalLinksAndCorruption(t *testing.T) {
	for _, header := range []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Size: 0}, {Name: "database.sqlite", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, {Name: "unlisted", Typeflag: tar.TypeReg, Size: 0}} {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.gz")
		f, _ := os.Create(path)
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(header)
		tw.Close()
		gz.Close()
		f.Close()
		if e := Restore(path, filepath.Join(dir, "result.db"), ""); e == nil {
			t.Fatal("unsafe archive accepted")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.gz")
	os.WriteFile(path, []byte("broken"), 0600)
	if e := Restore(path, filepath.Join(dir, "result.db"), ""); e == nil {
		t.Fatal("corruption accepted")
	}
}
