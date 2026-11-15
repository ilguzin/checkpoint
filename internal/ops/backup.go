package ops

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ilguzin/checkpoint/internal/core"
)

const maxArchive = 64 << 30

type BackupManifest struct {
	Format  int               `json:"format"`
	Schema  int               `json:"schema"`
	Created string            `json:"created"`
	Files   map[string]string `json:"files"`
}

func fileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, io.LimitReader(f, maxArchive+1)); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func Backup(database, output, configDir string) error {
	if database == output {
		return errors.New("backup destination equals database")
	}
	store, e := core.OpenStore(database)
	if e != nil {
		return e
	}
	defer store.Close()
	temp, e := os.MkdirTemp(filepath.Dir(output), ".checkpoint-backup-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(temp)
	snapshot := filepath.Join(temp, "database.sqlite")
	f, e := os.OpenFile(snapshot, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	f.Close()
	if e = store.Snapshot(snapshot); e != nil {
		return e
	}
	paths := map[string]string{"database.sqlite": snapshot}
	snapshotStore, e := core.OpenReadOnlyStore(snapshot)
	if e != nil {
		return e
	}
	mediaFiles, e := snapshotStore.MediaBackupFiles()
	snapshotStore.Close()
	if e != nil {
		return e
	}
	for name, path := range mediaFiles {
		paths[name] = path
	}
	if configDir != "" {
		e = filepath.WalkDir(configDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			info, e := d.Info()
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() {
				return errors.New("configuration contains non-regular file")
			}
			rel, e := filepath.Rel(configDir, path)
			if e != nil {
				return e
			}
			if info.Size() > 16<<20 || len(paths) > 1024 {
				return errors.New("configuration exceeds backup limits")
			}
			target := filepath.Join(temp, "config", rel)
			if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
				return e
			}
			src, e := os.Open(path)
			if e != nil {
				return e
			}
			defer src.Close()
			dst, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return e
			}
			n, copyErr := io.Copy(dst, io.LimitReader(src, (16<<20)+1))
			closeErr := dst.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n > 16<<20 {
				return errors.New("configuration grew beyond limit")
			}
			paths["config/"+filepath.ToSlash(rel)] = target
			return nil
		})
		if e != nil {
			return e
		}
	}
	manifest := BackupManifest{Format: 1, Schema: core.SchemaVersion, Created: time.Now().UTC().Format(time.RFC3339), Files: map[string]string{}}
	var total int64
	for name, path := range paths {
		info, e := os.Stat(path)
		if e != nil {
			return e
		}
		total += info.Size()
		if total > maxArchive || len(paths) > 1024 {
			return errors.New("backup exceeds size/file limit")
		}
		sum, e := fileHash(path)
		if e != nil {
			return e
		}
		manifest.Files[name] = sum
	}
	manifestData, e := json.Marshal(manifest)
	if e != nil {
		return e
	}
	out, e := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	success := false
	defer func() {
		out.Close()
		if !success {
			os.Remove(output)
		}
	}()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	for name, path := range paths {
		src, e := os.Open(path)
		if e != nil {
			return e
		}
		info, e := src.Stat()
		if e != nil {
			src.Close()
			return e
		}
		e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: info.Size(), Typeflag: tar.TypeReg})
		if e == nil {
			_, e = io.Copy(tw, src)
		}
		src.Close()
		if e != nil {
			return e
		}
	}
	if e = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(manifestData)), Typeflag: tar.TypeReg}); e != nil {
		return e
	}
	if _, e = tw.Write(manifestData); e != nil {
		return e
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = out.Sync(); e != nil {
		return e
	}
	success = true
	return nil
}
func Restore(input, destination, configOutput string) error {
	return RestoreWithMedia(input, destination, configOutput, "")
}
func RestoreWithMedia(input, destination, configOutput, deletionLedger string) error {
	if _, e := os.Lstat(destination); !errors.Is(e, os.ErrNotExist) {
		return errors.New("restore destination must not exist; restore separately and switch offline")
	}
	if configOutput != "" {
		if _, e := os.Lstat(configOutput); !errors.Is(e, os.ErrNotExist) {
			return errors.New("configuration destination must not exist")
		}
	}
	f, e := os.Open(input)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(io.LimitReader(f, maxArchive))
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	temp, e := os.MkdirTemp(filepath.Dir(destination), ".checkpoint-restore-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(temp)
	files := map[string]string{}
	var manifestData []byte
	var total int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		name := h.Name
		if h.Typeflag != tar.TypeReg || name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || filepath.ToSlash(filepath.Clean(name)) != name || name == ".." || strings.HasPrefix(name, "../") || h.Size < 0 {
			return errors.New("unsafe backup entry")
		}
		if name != "database.sqlite" && name != "manifest.json" && !strings.HasPrefix(name, "config/") && !strings.HasPrefix(name, "recordings/") {
			return errors.New("unexpected backup entry")
		}
		if _, exists := files[name]; exists {
			return errors.New("duplicate backup entry")
		}
		total += h.Size
		if total > maxArchive || len(files) > 1024 {
			return errors.New("backup too large")
		}
		path := filepath.Join(temp, filepath.FromSlash(name))
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return e
		}
		out, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, tr)
		closeErr := out.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		files[name] = path
		if name == "manifest.json" {
			if h.Size > 1<<20 {
				return errors.New("manifest too large")
			}
			manifestData, e = os.ReadFile(path)
			if e != nil {
				return e
			}
		}
	}
	if n, err := io.Copy(io.Discard, io.LimitReader(gz, (1<<20)+1)); err != nil || n > 1<<20 {
		return errors.New("invalid backup gzip trailer or excessive trailing data")
	}
	var manifest BackupManifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestData)))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&manifest); e != nil {
		return e
	}
	if e = decoder.Decode(new(any)); e != io.EOF {
		return errors.New("trailing manifest data")
	}
	if manifest.Format != 1 || manifest.Schema != core.SchemaVersion || len(files) != len(manifest.Files)+1 {
		return errors.New("unsupported or incomplete backup")
	}
	for name, want := range manifest.Files {
		path, ok := files[name]
		if !ok {
			return errors.New("missing backup member")
		}
		got, e := fileHash(path)
		if e != nil {
			return e
		}
		if got != want {
			return fmt.Errorf("backup hash mismatch: %s", name)
		}
	}
	database, ok := files["database.sqlite"]
	if !ok {
		return errors.New("database missing")
	}
	check, e := core.OpenReadOnlyStore(database)
	if e != nil {
		return e
	}
	check.Close()
	mediaOutput := destination + ".recordings"
	mediaDir := filepath.Join(temp, "recordings")
	mediaMoved := false
	if _, err := os.Stat(mediaDir); err == nil {
		if deletionLedger == "" {
			return errors.New("recordings restore requires --deletion-ledger with the current deletion journal; an old backup alone cannot prove which rooms were deleted")
		}
		entries, e := os.ReadDir(deletionLedger)
		if e != nil {
			return e
		}
		target := filepath.Join(mediaDir, "deletions")
		if e = os.MkdirAll(target, 0700); e != nil {
			return e
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				return errors.New("invalid current deletion ledger")
			}
			body, e := os.ReadFile(filepath.Join(deletionLedger, entry.Name()))
			if e != nil {
				return e
			}
			if e = os.WriteFile(filepath.Join(target, entry.Name()), body, 0600); e != nil {
				return e
			}
		}
		if _, e = os.Lstat(mediaOutput); !os.IsNotExist(e) {
			return errors.New("recordings destination must not exist")
		}
		restored, e := core.OpenStore(database)
		if e != nil {
			return e
		}
		e = restored.RebindRecordingStorage(mediaOutput)
		restored.Close()
		if e != nil {
			return e
		}
		if e = os.Rename(mediaDir, mediaOutput); e != nil {
			return e
		}
		mediaMoved = true
		defer func() {
			if mediaMoved {
				_ = os.Rename(mediaOutput, mediaDir)
			}
		}()
	}
	configMoved := false
	if configOutput != "" {
		config := filepath.Join(temp, "config")
		if _, e = os.Stat(config); e == nil {
			if e = os.Rename(config, configOutput); e != nil {
				return e
			}
			configMoved = true
		}
	}
	// Hard link is atomic and fails if a competing process creates the destination.
	if e = os.Link(database, destination); e != nil {
		if configMoved {
			_ = os.Rename(configOutput, filepath.Join(temp, "config"))
		}
		return e
	}
	mediaMoved = false
	return nil
}
