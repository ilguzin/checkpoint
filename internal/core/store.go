package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 5

//go:embed migrations/*.sql
var migrations embed.FS
var ErrAccess = errors.New("access unavailable")

type Session struct {
	Identity   string    `json:"identity"`
	Name       string    `json:"name"`
	Room       string    `json:"room"`
	Role       string    `json:"role"`
	MediaRoom  string    `json:"mediaRoom"`
	Admitted   bool      `json:"admitted"`
	CanScreen  bool      `json:"canScreen"`
	HandAt     int64     `json:"handAt"`
	Generation int       `json:"generation"`
	Moderator  bool      `json:"moderator"`
	CanPublish bool      `json:"canPublish"`
	Expires    time.Time `json:"expires"`
	SecretHash string    `json:"secretHash"`
	InviteID   string    `json:"inviteId,omitempty"`
	UserID     string    `json:"userId,omitempty"`
}

type Store struct {
	db   *sql.DB
	path string
}

func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func OpenStore(path string) (*Store, error)         { return openStore(path, false) }
func OpenReadOnlyStore(path string) (*Store, error) { return openStore(path, true) }
func openStore(path string, readonly bool) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if !readonly {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e == nil {
			_ = f.Close()
		} else if !errors.Is(e, os.ErrExist) {
			return nil, e
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database must be a regular private file (0600)")
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	if readonly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("mode", "rw")
		q.Set("_txlock", "immediate")
		q.Add("_pragma", "synchronous(FULL)")
		q.Add("_pragma", "secure_delete(ON)")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err = s.checkSchema(readonly); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) checkSchema(readonly bool) error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > SchemaVersion || (readonly && version != SchemaVersion) {
		return fmt.Errorf("unsupported database schema %d (binary requires %d)", version, SchemaVersion)
	}
	if readonly {
		return nil
	}
	// DELETE journal permits a genuinely read-only signal mount: no WAL/shm writes.
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > SchemaVersion {
		return errors.New("database newer than binary")
	}
	if version == 0 {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("unversioned nonempty database; refusing migration")
		}
		schema, err := migrations.ReadFile("migrations/001_foundation.sql")
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(schema)); err != nil {
			return err
		}
		if _, err = tx.Exec("PRAGMA user_version=1"); err != nil {
			return err
		}
	}
	if version < 2 {
		schema, e := migrations.ReadFile("migrations/002_meetings.sql")
		if e != nil {
			return e
		}
		if _, e = tx.Exec(string(schema)); e != nil {
			return e
		}
		if _, e = tx.Exec("PRAGMA user_version=2"); e != nil {
			return e
		}
	}
	if version < 3 {
		schema, e := migrations.ReadFile("migrations/003_room_admins.sql")
		if e != nil {
			return e
		}
		if _, e = tx.Exec(string(schema)); e != nil {
			return e
		}
		if _, e = tx.Exec("PRAGMA user_version=3"); e != nil {
			return e
		}
	}
	if version < 4 {
		schema, e := migrations.ReadFile("migrations/004_recordings.sql")
		if e != nil {
			return e
		}
		if _, e = tx.Exec(string(schema)); e != nil {
			return e
		}
		if _, e = tx.Exec("PRAGMA user_version=4"); e != nil {
			return e
		}
	}
	if version < 5 {
		schema, e := migrations.ReadFile("migrations/005_ui_locale.sql")
		if e != nil {
			return e
		}
		if _, e = tx.Exec(string(schema)); e != nil {
			return e
		}
		if _, e = tx.Exec("PRAGMA user_version=5"); e != nil {
			return e
		}
	}

	return tx.Commit()
}
func (s *Store) Healthy() bool {
	var v int
	return s.db.QueryRow("PRAGMA user_version").Scan(&v) == nil && v == SchemaVersion
}

type rowScanner interface{ Scan(...any) error }

func scanSession(row rowScanner) (Session, error) {
	var p Session
	var expires int64
	err := row.Scan(&p.Identity, &p.Name, &p.Room, &p.Role, &p.CanPublish, &expires, &p.SecretHash, &p.InviteID, &p.UserID, &p.MediaRoom, &p.Admitted, &p.CanScreen, &p.HandAt, &p.Generation)
	p.Expires = time.Unix(expires, 0)
	p.Moderator = p.Role == "owner" || p.Role == "moderator"
	return p, err
}

const sessionSelect = `SELECT s.identity,s.name,s.room_id,s.role,s.can_publish,s.expires,s.secret_hash,COALESCE(s.invite_id,''),COALESCE(s.user_id,''),CASE WHEN s.generation=0 THEN s.room_id ELSE s.room_id||'--'||s.generation END,s.admitted,(r.screen_identity=s.identity AND r.screen_until>unixepoch()),s.hand_at,s.generation FROM sessions s JOIN rooms r ON r.id=s.room_id WHERE s.generation=r.generation AND s.revoked=0 AND s.expires>unixepoch() AND r.closed=0 AND r.deletion_state='' AND (s.invite_id IS NULL OR EXISTS(SELECT 1 FROM invites i WHERE i.id=s.invite_id AND i.revoked=0 AND i.expires>unixepoch())) AND (s.user_id IS NULL OR EXISTS(SELECT 1 FROM users u WHERE u.id=s.user_id AND u.disabled=0 AND ((s.role='owner' AND r.owner_id=u.id) OR (s.role='moderator' AND EXISTS(SELECT 1 FROM room_admins a WHERE a.room_id=s.room_id AND a.user_id=u.id)) OR s.role IN ('attendee','presenter')))) AND NOT EXISTS(SELECT 1 FROM room_bans b WHERE b.room_id=s.room_id AND b.user_id=s.user_id)`

func (s *Store) lookup(id string) (Session, error) {
	return scanSession(s.db.QueryRow(sessionSelect+" AND s.identity=?", id))
}
func (s *Store) Get(id string) (Session, bool) {
	p, e := s.lookup(id)
	return p, e == nil
}
func (s *Store) Authenticate(secret string) (Session, bool) {
	if len(secret) != 64 {
		return Session{}, false
	}
	p, e := scanSession(s.db.QueryRow(sessionSelect+" AND s.secret_hash=?", hash(secret)))
	return p, e == nil
}
func insertSession(tx *sql.Tx, name, room, role, invite, user string, publish bool, expires int64) (Session, string, error) {
	if maintenance(tx) {
		return Session{}, "", ErrAccess
	}
	var generation int
	var waiting bool
	if err := tx.QueryRow("SELECT generation,waiting_room FROM rooms WHERE id=?", room).Scan(&generation, &waiting); err != nil {
		return Session{}, "", err
	}
	admitted := !waiting || role == "owner" || role == "moderator"
	media := room
	if generation > 0 {
		media = fmt.Sprintf("%s--%d", room, generation)
	}
	secret := randomID()
	p := Session{MediaRoom: media, Admitted: admitted, Generation: generation, Identity: randomID(), Name: name, Room: room, Role: role, Moderator: role == "owner" || role == "moderator", CanPublish: publish, Expires: time.Unix(expires, 0), SecretHash: hash(secret), InviteID: invite, UserID: user}
	_, err := tx.Exec(`INSERT INTO sessions(identity,name,room_id,role,can_publish,expires,secret_hash,invite_id,user_id,generation,admitted) VALUES(?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?)`, p.Identity, name, room, role, publish, expires, p.SecretHash, invite, user, generation, admitted)
	if err == nil && p.UserID != "" && p.Admitted {
		err = grantMembership(tx, p)
	}
	return p, secret, err
}

// Add is used by migration/regression fixtures. User requests use JoinInvite/JoinRoom.
func (s *Store) Add(name, room string, moderator, publish bool) (Session, string, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return Session{}, "", e
	}
	defer tx.Rollback()
	_, e = tx.Exec("INSERT OR IGNORE INTO rooms(id,name,mode) VALUES(?,?,'meeting')", room, room)
	if e != nil {
		return Session{}, "", e
	}
	role := "attendee"
	if moderator {
		role = "moderator"
	} else if publish {
		role = "presenter"
	}
	p, secret, e := insertSession(tx, name, room, role, "", "", publish, time.Now().Add(12*time.Hour).Unix())
	if e == nil {
		e = tx.Commit()
	}
	return p, secret, e
}
func (s *Store) SetPublish(id string, publish bool) error {
	_, err := s.db.Exec("UPDATE sessions SET can_publish=?,role=CASE WHEN ? THEN 'presenter' ELSE 'attendee' END WHERE identity=? AND revoked=0", publish, publish, id)
	return err
}
func (s *Store) RoomIDs() ([]string, error) {
	rows, e := s.db.Query("SELECT id FROM rooms UNION SELECT room_id||'--'||generation FROM sessions WHERE generation>0")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Seed only explicitly configured prototype invitations. Revocation is never reset.
func (s *Store) SeedInvites(invites []Invite) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, inv := range invites {
		mode := "meeting"
		if strings.Contains(inv.Room, "webinar") {
			mode = "webinar"
		}
		if _, e = tx.Exec("INSERT OR IGNORE INTO rooms(id,name,mode) VALUES(?,?,?)", inv.Room, inv.Room, mode); e != nil {
			return e
		}
		role := "attendee"
		if inv.Moderator {
			role = "moderator"
		} else if inv.CanPublish {
			role = "presenter"
		}
		if _, e = tx.Exec("INSERT OR IGNORE INTO invites(id,secret_hash,room_id,role,expires,max_uses) VALUES(?,?,?,?,?,0)", hash(inv.Code), hash(inv.Code), inv.Room, role, time.Now().Add(30*24*time.Hour).Unix()); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// ImportLegacy is explicit, atomic and one-shot. It never modifies the source JSON.
func (s *Store) ImportLegacy(path string) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if len(b) > 2<<20 {
		return errors.New("legacy store too large")
	}
	var sessions map[string]Session
	if e = json.Unmarshal(b, &sessions); e != nil {
		return e
	}
	if sessions == nil {
		return errors.New("invalid legacy store")
	}
	tx, e := s.db.BeginTx(context.Background(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRow("SELECT (SELECT count(*) FROM sessions)+(SELECT count(*) FROM metadata WHERE key='legacy_import')").Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return errors.New("legacy import requires empty sessions and no earlier import")
	}
	for id, p := range sessions {
		if id != p.Identity || len(id) != 64 || len(p.SecretHash) != 64 || p.Room == "" {
			return errors.New("invalid legacy session")
		}
		role := "attendee"
		if p.Moderator {
			role = "moderator"
		} else if p.CanPublish {
			role = "presenter"
		}
		if _, e = tx.Exec("INSERT INTO sessions(identity,name,room_id,role,can_publish,expires,secret_hash) VALUES(?,?,?,?,?,?,?)", id, p.Name, p.Room, role, p.CanPublish, p.Expires.Unix(), p.SecretHash); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("INSERT INTO metadata VALUES('legacy_import',?)", hash(string(b))); e != nil {
		return e
	}
	return tx.Commit()
}
