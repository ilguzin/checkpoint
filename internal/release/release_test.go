package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"
	"github.com/ilguzin/checkpoint/internal/core"
)

func fixtureManifest() Manifest {
	return Manifest{Format: 1, Project: "checkpoint", Repository: Repository, Channel: "stable", Sequence: 2, Version: "v0.2.0", LiveKitVersion: "1.13.11", Schema: Schema{core.SchemaVersion, core.SchemaVersion, core.SchemaVersion}, Assets: []Asset{{Name: "checkpoint-linux-amd64.tar.gz", OS: "linux", Arch: "amd64", Size: 3, SHA256: Hash([]byte("abc")), Kind: "checkpoint"}}}
}
func TestSignaturesIdentityReplayAndDowngrade(t *testing.T) {
	pub, key, e := minisign.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	other, _, _ := minisign.GenerateKey(rand.Reader)
	m := fixtureManifest()
	body, _ := json.Marshal(m)
	signature := minisign.Sign(key, body)
	if _, e = Verify(body, signature, pub.String(), State{}); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(append(body, ' '), signature, pub.String(), State{}); e == nil {
		t.Fatal("tampered bytes accepted")
	}
	if _, e = Verify(body, signature, other.String(), State{}); e == nil {
		t.Fatal("wrong key accepted")
	}
	for _, state := range []State{{Sequence: 3}, {Sequence: 2, Digest: strings.Repeat("a", 64)}, {Sequence: 1, Version: "v0.3.0"}} {
		if _, e = Verify(body, signature, pub.String(), state); e == nil {
			t.Fatal("downgrade/reused sequence accepted")
		}
	}
	if _, e = Verify(body, signature, pub.String(), State{Sequence: 2, Digest: Hash(body), Version: m.Version}); e != nil {
		t.Fatal("idempotent check failed", e)
	}
	for _, change := range []func(*Manifest){func(m *Manifest) { m.Repository = "other/repo" }, func(m *Manifest) { m.Assets[0].Name = "../checkpoint" }, func(m *Manifest) { m.Assets = append(m.Assets, m.Assets[0]) }, func(m *Manifest) { m.Schema.Min = m.Schema.Max + 1 }} {
		bad := fixtureManifest()
		change(&bad)
		body, _ := json.Marshal(bad)
		if _, e = Verify(body, minisign.Sign(key, body), pub.String(), State{}); e == nil {
			t.Fatal("invalid signed manifest accepted")
		}
	}
}
func testBundle(version string) []byte {
	binary := make([]byte, 24)
	copy(binary, []byte{0x7f, 'E', 'L', 'F', 2, 1})
	binary[18] = 62
	files := map[string][]byte{"checkpoint": binary, "LICENSE": []byte("fixture license")}
	m := Bundle{Format: 1, Platform: "linux-amd64", Commit: strings.Repeat("a", 40), Version: version, Files: map[string]string{}}
	for k, v := range files {
		m.Files[k] = Hash(v)
	}
	files["manifest.json"], _ = json.Marshal(m)
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for k, v := range files {
		tw.WriteHeader(&tar.Header{Name: k, Size: int64(len(v)), Mode: 0644, Typeflag: tar.TypeReg})
		tw.Write(v)
	}
	tw.Close()
	gz.Close()
	return output.Bytes()
}
func TestBundleTraversalAndCorruption(t *testing.T) {
	if _, _, e := Unpack(testBundle("v0.2.0"), "v0.2.0"); e != nil {
		t.Fatal(e)
	}
	if _, _, e := Unpack(testBundle("v0.2.0"), "v0.3.0"); e == nil {
		t.Fatal("version mismatch accepted")
	}
	for _, entry := range []*tar.Header{{Name: "../file", Typeflag: tar.TypeReg}, {Name: "checkpoint", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}} {
		var out bytes.Buffer
		gz := gzip.NewWriter(&out)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(entry)
		tw.Close()
		gz.Close()
		if _, _, e := Unpack(out.Bytes(), "v0.2.0"); e == nil {
			t.Fatal("unsafe archive accepted")
		}
	}
}
func installFixture(t *testing.T) (InstallConfig, *bool, string, string) {
	t.Helper()
	dir := t.TempDir()
	releases := filepath.Join(dir, "releases")
	old := filepath.Join(releases, "v0.1.0")
	os.MkdirAll(old, 0755)
	current := filepath.Join(dir, "current")
	os.Symlink(old, current)
	config := filepath.Join(dir, "config")
	os.Mkdir(config, 0700)
	database := filepath.Join(dir, "checkpoint.db")
	store, e := core.OpenStore(database)
	if e != nil {
		t.Fatal(e)
	}
	_, secret, e := store.Add("Existing participant", "meeting", false, true)
	store.Close()
	if e != nil {
		t.Fatal(e)
	}
	maintained := new(bool)
	c := InstallConfig{Releases: releases, Current: current, Database: database, ConfigDir: config, Backups: filepath.Join(dir, "backups"), State: filepath.Join(config, "update-state.json"), Journal: filepath.Join(config, "journal.json")}
	c.Maintenance = func(on bool) error { *maintained = on; return nil }
	c.Idle = func() error {
		if !*maintained {
			t.Fatal("idle check before blocking joins")
		}
		return nil
	}
	c.Health = func() error { return nil }
	c.Service = func(string, ...string) error {
		if !*maintained {
			t.Fatal("service mutation outside maintenance")
		}
		return nil
	}
	return c, maintained, old, secret
}
func TestApplySuccessAndRollbackPreserveDatabase(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "rollback", false: "commit"}[fail], func(t *testing.T) {
			c, maintained, old, secret := installFixture(t)
			calls := 0
			c.Health = func() error {
				calls++
				if fail && calls == 1 {
					return errors.New("candidate not healthy")
				}
				return nil
			}
			m := fixtureManifest()
			body, _ := json.Marshal(m)
			e := Apply(c, m, body, testBundle(m.Version))
			target, _ := os.Readlink(c.Current)
			if fail {
				if e == nil || target != old {
					t.Fatal("rollback failed", e)
				}
			} else {
				if e != nil || target == old {
					t.Fatal("update failed", e)
				}
				state, e := ReadState(c.State)
				if e != nil || state.Sequence != m.Sequence || state.Digest != Hash(body) {
					t.Fatal("acceptance not persisted")
				}
			}
			if *maintained {
				t.Fatal("maintenance not cleared")
			}
			if _, e = os.Stat(c.Journal); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("journal not cleared")
			}
			store, e := core.OpenStore(c.Database)
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			if _, ok := store.Authenticate(secret); !ok {
				t.Fatal("database writes lost")
			}
		})
	}
}
func TestBusyCallDefersWithoutSwitchAndRecoverAfterInterruption(t *testing.T) {
	c, maintained, old, _ := installFixture(t)
	m := fixtureManifest()
	raw, _ := json.Marshal(m)
	c.Idle = func() error { return errors.New("active call") }
	if e := Apply(c, m, raw, testBundle(m.Version)); e == nil {
		t.Fatal("active call interrupted")
	}
	target, _ := os.Readlink(c.Current)
	if target != old || *maintained {
		t.Fatal("deferred update changed active state")
	}
	next := filepath.Join(c.Releases, "v0.2.0")
	os.Mkdir(next, 0755)
	switchLink(c.Current, next)
	AtomicJSON(c.Journal, journal{Old: old, Next: next, Candidate: State{Sequence: 2, Digest: Hash(raw), Version: m.Version}})
	if e := Recover(c); e != nil {
		t.Fatal(e)
	}
	target, _ = os.Readlink(c.Current)
	if target != old || *maintained {
		t.Fatal("uncommitted update not recovered")
	}
}

func TestRecoveryRemovesOnlyJournalOwnedPendingLink(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		c, _, old, _ := installFixture(t)
		next := filepath.Join(c.Releases, "v0.2.0")
		os.Mkdir(next, 0755)
		AtomicJSON(c.Journal, journal{Old: old, Next: next, Candidate: State{Sequence: 2}})
		pending := next
		if foreign {
			pending = "/foreign"
		}
		os.Symlink(pending, c.Current+".next")
		err := Recover(c)
		if foreign {
			if err == nil {
				t.Fatal("foreign link removed")
			}
			got, _ := os.Readlink(c.Current + ".next")
			if got != pending {
				t.Fatal("foreign link changed")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
func TestCandidateTrustKeyFailureLeavesServicesUntouched(t *testing.T) {
	c, maintained, old, _ := installFixture(t)
	c.ValidateCandidate = func(string) error { return errors.New("wrong key") }
	m := fixtureManifest()
	raw, _ := json.Marshal(m)
	if err := Apply(c, m, raw, testBundle(m.Version)); err == nil {
		t.Fatal("candidate with wrong key accepted")
	}
	target, _ := os.Readlink(c.Current)
	if target != old || *maintained {
		t.Fatal("invalid candidate affected services")
	}
}
func TestBundleRejectsBrokenGzipChecksum(t *testing.T) {
	data := testBundle("v0.2.0")
	data[len(data)-8] ^= 0xff
	if _, _, err := Unpack(data, "v0.2.0"); err == nil {
		t.Fatal("bad gzip checksum accepted")
	}
}

// The fixture follows the current schema; incompatible targets must still fail
// before maintenance, service changes, or database writes.
func TestApplyRejectsSchemaChangesWithoutTouchingInstallation(t *testing.T) {
	for _, target := range []int{core.SchemaVersion - 1, core.SchemaVersion + 1} {
		c, maintained, old, _ := installFixture(t)
		before, err := os.ReadFile(c.Database)
		if err != nil {
			t.Fatal(err)
		}
		c.Maintenance = func(bool) error { t.Fatal("schema rejection entered maintenance"); return nil }
		c.Service = func(string, ...string) error { t.Fatal("schema rejection changed services"); return nil }
		m := fixtureManifest()
		m.Schema = Schema{target, target, target}
		raw, _ := json.Marshal(m)
		if err := Apply(c, m, raw, testBundle(m.Version)); err == nil || !strings.Contains(err.Error(), "schema-changing updates") {
			t.Fatalf("schema %d update was not explicitly rejected: %v", target, err)
		}
		current, _ := os.Readlink(c.Current)
		after, err := os.ReadFile(c.Database)
		if err != nil || !bytes.Equal(before, after) || current != old || *maintained {
			t.Fatal("schema rejection changed the installed version or database")
		}
		for _, path := range []string{c.Journal, c.State} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("schema rejection created update state")
			}
		}
	}
}
