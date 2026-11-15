package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ilguzin/checkpoint/internal/core"
	"github.com/ilguzin/checkpoint/internal/ops"
	"golang.org/x/sys/unix"
)

type Bundle struct {
	Format   int               `json:"format"`
	Platform string            `json:"platform"`
	Commit   string            `json:"commit"`
	Version  string            `json:"version"`
	Files    map[string]string `json:"files"`
}

func Unpack(data []byte, version string) (Bundle, map[string][]byte, error) {
	var manifest Bundle
	files := map[string][]byte{}
	gz, e := gzip.NewReader(bytes.NewReader(data))
	if e != nil {
		return manifest, nil, e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return manifest, nil, e
		}
		n := h.Name
		if h.Typeflag != tar.TypeReg || n == "" || filepath.Clean(n) != n || strings.HasPrefix(n, "/") || strings.HasPrefix(n, "../") || strings.Contains(n, "\\") || n == ".." || h.Size < 0 {
			return manifest, nil, errors.New("unsafe bundle entry")
		}
		if _, ok := files[n]; ok {
			return manifest, nil, errors.New("duplicate bundle entry")
		}
		if n != "checkpoint" && n != "LICENSE" && n != "manifest.json" && n != "setup.sh" && n != "scripts/transcribe.py" && n != "scripts/minutes-assets.py" && n != "scripts/minutes-setup.py" && n != "deploy/minutes/models.lock.json" && n != "deploy/minutes/requirements.lock" && !strings.HasPrefix(n, "third_party/") {
			return manifest, nil, errors.New("unexpected bundle entry")
		}
		total += h.Size
		if total > MaxAsset || len(files) > 4096 {
			return manifest, nil, errors.New("expanded bundle exceeds limits")
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			return manifest, nil, e
		}
		files[n] = b
	}
	if n, err := io.Copy(io.Discard, io.LimitReader(gz, (1<<20)+1)); err != nil || n > 1<<20 {
		return manifest, nil, errors.New("invalid gzip trailer or excessive trailing data")
	}
	dec := json.NewDecoder(bytes.NewReader(files["manifest.json"]))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&manifest); e != nil {
		return manifest, nil, e
	}
	if e = dec.Decode(new(any)); e != io.EOF {
		return manifest, nil, errors.New("trailing bundle manifest")
	}
	delete(files, "manifest.json")
	if manifest.Format != 1 || manifest.Platform != "linux-amd64" || manifest.Version != version || !regexpCommit(manifest.Commit) || len(manifest.Files) != len(files) {
		return manifest, nil, errors.New("bundle identity mismatch")
	}
	for name, body := range files {
		if manifest.Files[name] != Hash(body) {
			return manifest, nil, errors.New("bundle hash mismatch")
		}
	}
	binary := files["checkpoint"]
	if len(binary) < 20 || !bytes.Equal(binary[:5], []byte{0x7f, 'E', 'L', 'F', 2}) || binary[5] != 1 || binary[18] != 62 || binary[19] != 0 {
		return manifest, nil, errors.New("not a Linux amd64 ELF binary")
	}
	if len(files["LICENSE"]) == 0 {
		return manifest, nil, errors.New("license missing")
	}
	return manifest, files, nil
}
func regexpCommit(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func AtomicJSON(path string, v any) error {
	body, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomicFile(path, append(body, '\n'), 0600)
}
func atomicFile(path string, body []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".checkpoint-state-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(body)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func ReadState(path string) (State, error) {
	var s State
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	return s, e
}
func Lock(path string) (func(), error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("another update is running")
	}
	return func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
}

type InstallConfig struct {
	Releases, Current, Database, ConfigDir, Backups, State, Journal, SetupState string
	Service                                                                     func(string, ...string) error
	Maintenance                                                                 func(bool) error
	Idle                                                                        func() error
	Health                                                                      func() error
	ValidateCandidate                                                           func(string) error
}
type journal struct {
	Old       string          `json:"old"`
	Next      string          `json:"next"`
	Candidate State           `json:"candidate"`
	OldSetup  json.RawMessage `json:"oldSetup,omitempty"`
	NewSetup  json.RawMessage `json:"newSetup,omitempty"`
}

func switchLink(link, target string) error {
	temporary := link + ".next"
	if _, e := os.Lstat(temporary); e == nil {
		return errors.New("stale switch link; inspect recovery journal")
	}
	if e := os.Symlink(target, temporary); e != nil {
		return e
	}
	defer os.Remove(temporary)
	if e := os.Rename(temporary, link); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(link))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func Apply(c InstallConfig, m Manifest, raw, bundle []byte) (result error) {
	if m.Schema.Target != core.SchemaVersion || m.Schema.Min > core.SchemaVersion || m.Schema.Max < core.SchemaVersion {
		return errors.New("schema-changing updates require an explicit migration/rollback procedure")
	}
	if m.LiveKitVersion != "1.13.11" {
		return errors.New("this updater supports LiveKit 1.13.11; media upgrades require a separate maintenance deployment")
	}
	if _, e := os.Lstat(c.Journal); !errors.Is(e, os.ErrNotExist) {
		return errors.New("unfinished update exists; run update --recover")
	}
	bm, files, e := Unpack(bundle, m.Version)
	if e != nil {
		return e
	}
	old, e := os.Readlink(c.Current)
	if e != nil {
		return e
	}
	if !filepath.IsAbs(old) {
		old = filepath.Join(filepath.Dir(c.Current), old)
	}
	old, e = filepath.Abs(old)
	if e != nil || !strings.HasPrefix(old, filepath.Clean(c.Releases)+string(os.PathSeparator)) {
		return errors.New("current release outside managed directory")
	}
	if e = os.MkdirAll(c.Releases, 0755); e != nil {
		return e
	}
	next := filepath.Join(c.Releases, m.Version+"-"+bm.Files["checkpoint"][:12])
	if _, e = os.Lstat(next); e == nil {
		return errors.New("candidate directory exists; inspect it before retrying")
	}
	stage, e := os.MkdirTemp(c.Releases, ".stage-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	os.Chmod(stage, 0755)
	for name, body := range files {
		path := filepath.Join(stage, name)
		if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			return e
		}
		mode := os.FileMode(0644)
		if name == "checkpoint" || name == "setup.sh" {
			mode = 0755
		}
		if e = atomicFile(path, body, mode); e != nil {
			return e
		}
	}
	if c.ValidateCandidate != nil {
		if e = c.ValidateCandidate(filepath.Join(stage, "checkpoint")); e != nil {
			return e
		}
	}
	if e = os.Rename(stage, next); e != nil {
		return e
	}
	journalWritten := false
	defer func() {
		if !journalWritten {
			_ = os.RemoveAll(next)
		}
	}()
	if e = c.Maintenance(true); e != nil {
		return e
	}
	keepMaintenance := false
	defer func() {
		if !keepMaintenance {
			if e := c.Maintenance(false); e != nil {
				result = fmt.Errorf("maintenance could not be cleared: %w", e)
			}
		}
	}()
	if e = c.Idle(); e != nil {
		return e
	}
	if e = os.MkdirAll(c.Backups, 0700); e != nil {
		return e
	}
	backup := filepath.Join(c.Backups, "pre-update-"+time.Now().UTC().Format("20060102T150405.000000000")+".tar.gz")
	if e = ops.Backup(c.Database, backup, c.ConfigDir); e != nil {
		return e
	}
	j := journal{Old: old, Next: next, Candidate: State{Sequence: m.Sequence, Digest: Hash(raw), Version: m.Version}}
	if c.SetupState != "" {
		j.OldSetup, e = os.ReadFile(c.SetupState)
		if e != nil {
			return e
		}
		var setup map[string]any
		if e = json.Unmarshal(j.OldSetup, &setup); e != nil {
			return e
		}
		links, ok := setup["links"].(map[string]any)
		if !ok || links[c.Current] != old {
			return errors.New("setup metadata does not match active release")
		}
		managed, ok := setup["files"].(map[string]any)
		if !ok {
			return errors.New("invalid setup file inventory")
		}
		for name, body := range files {
			managed[filepath.Join(next, name)] = Hash(body)
		}
		links[c.Current] = next
		setup["manifest"] = bm
		setup["bundle_sha256"] = Hash(bundle)
		j.NewSetup, e = json.MarshalIndent(setup, "", "  ")
		if e != nil {
			return e
		}
	}
	if e = AtomicJSON(c.Journal, j); e != nil {
		return e
	}
	journalWritten = true
	keepMaintenance = true
	rollback := func(cause error) error {
		_ = c.Service("stop", "checkpoint-core", "checkpoint-signal", "checkpoint-observer")
		if e := switchLink(c.Current, old); e != nil {
			return fmt.Errorf("update failed (%v); rollback requires recovery: %w", cause, e)
		}
		if e := c.Service("start", "checkpoint-core", "checkpoint-signal", "checkpoint-observer"); e != nil {
			return fmt.Errorf("rollback start failed: %w", e)
		}
		if e := c.Health(); e != nil {
			return fmt.Errorf("rollback health failed: %w", e)
		}
		if e := c.Maintenance(false); e != nil {
			return e
		}
		if e := os.Remove(c.Journal); e != nil {
			return e
		}
		_ = os.RemoveAll(next)
		return fmt.Errorf("update rolled back without restoring old database writes: %w", cause)
	}
	if e = c.Service("stop", "checkpoint-core", "checkpoint-signal"); e != nil {
		return rollback(e)
	}
	if e = switchLink(c.Current, next); e != nil {
		return rollback(e)
	}
	if e = c.Service("start", "checkpoint-core", "checkpoint-signal"); e != nil {
		return rollback(e)
	}
	if e = c.Service("restart", "checkpoint-observer"); e != nil {
		return rollback(e)
	}
	if e = c.Health(); e != nil {
		return rollback(e)
	}
	if e = AtomicJSON(c.State, j.Candidate); e != nil {
		return fmt.Errorf("update state commit uncertain; run update --recover: %w", e)
	}
	if c.SetupState != "" {
		if e = atomicFile(c.SetupState, j.NewSetup, 0600); e != nil {
			return e
		}
	}
	// Persist the accepted state before reopening writes. Recovery recognizes it.
	if e = c.Maintenance(false); e != nil {
		return e
	}
	keepMaintenance = false
	if e = os.Remove(c.Journal); e != nil {
		return e
	}
	return nil
}
func Recover(c InstallConfig) error {
	b, e := os.ReadFile(c.Journal)
	if e != nil {
		return e
	}
	var j journal
	if e = json.Unmarshal(b, &j); e != nil {
		return e
	}
	for _, p := range []string{j.Old, j.Next} {
		if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(c.Releases)+string(os.PathSeparator)) {
			return errors.New("unsafe recovery path")
		}
	}
	state, e := ReadState(c.State)
	if e != nil {
		return e
	}
	target := j.Old
	if state.Sequence == j.Candidate.Sequence && state.Digest == j.Candidate.Digest {
		target = j.Next
	}
	if e = c.Maintenance(true); e != nil {
		return e
	}
	if e = c.Service("stop", "checkpoint-core", "checkpoint-signal", "checkpoint-observer"); e != nil {
		return e
	}
	// A crash can leave the temporary symlink. Only remove a link named in
	// the durable journal; never remove an unexpected file or foreign link.
	if pending, err := os.Readlink(c.Current + ".next"); err == nil {
		if pending != j.Old && pending != j.Next {
			return errors.New("foreign pending switch link")
		}
		if e = os.Remove(c.Current + ".next"); e != nil {
			return e
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if e = switchLink(c.Current, target); e != nil {
		return e
	}
	if e = c.Service("start", "checkpoint-core", "checkpoint-signal", "checkpoint-observer"); e != nil {
		return e
	}
	if e = c.Health(); e != nil {
		return e
	}
	if c.SetupState != "" {
		setup := j.OldSetup
		if target == j.Next {
			setup = j.NewSetup
		}
		if len(setup) == 0 {
			return errors.New("recovery setup metadata missing")
		}
		if e = atomicFile(c.SetupState, setup, 0600); e != nil {
			return e
		}
	}
	if e = c.Maintenance(false); e != nil {
		return e
	}
	return os.Remove(c.Journal)
}
