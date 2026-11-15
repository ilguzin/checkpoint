package core

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	UILocale string `json:"uiLocale"`
	ID       string `json:"id"`
	Username string `json:"username"`
	Admin    bool   `json:"admin"`
	Disabled bool   `json:"disabled"`
}
type Room struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	OwnerID       string `json:"ownerId"`
	Closed        bool   `json:"closed"`
	AccessRole    string `json:"accessRole"`
	DeletionState string `json:"deletionState"`
}
type Invitation struct {
	ID      string `json:"id"`
	Room    string `json:"room"`
	Role    string `json:"role"`
	Expires int64  `json:"expires"`
	MaxUses int    `json:"maxUses"`
	Uses    int    `json:"uses"`
	Revoked bool   `json:"revoked"`
}

func passwordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 72 {
		return "", errors.New("password must contain 12–72 bytes")
	}
	b, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(b), e
}
func validUsername(name string) bool {
	if len(name) < 3 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func validRole(role string) bool {
	return role == "moderator" || role == "presenter" || role == "attendee"
}
func isAdmin(tx *sql.Tx, id string) bool {
	var admin bool
	return tx.QueryRow("SELECT admin FROM users WHERE id=? AND disabled=0", id).Scan(&admin) == nil && admin
}
func canOwn(tx *sql.Tx, user, room string) bool {
	var ok bool
	return tx.QueryRow("SELECT EXISTS(SELECT 1 FROM rooms r JOIN users u ON u.id=? WHERE r.id=? AND u.disabled=0 AND r.owner_id=u.id AND r.deletion_state='')", user, room).Scan(&ok) == nil && ok
}
func canManage(tx *sql.Tx, user, room string) bool {
	var ok bool
	return tx.QueryRow("SELECT EXISTS(SELECT 1 FROM rooms r JOIN users u ON u.id=? WHERE r.id=? AND r.deletion_state='' AND u.disabled=0 AND (r.owner_id=u.id OR EXISTS(SELECT 1 FROM room_admins a WHERE a.room_id=r.id AND a.user_id=u.id)))", user, room).Scan(&ok) == nil && ok
}
func (s *Store) CreateUser(actor, name, password string, admin, bootstrap bool) (User, error) {
	return s.createUser(actor, name, password, admin, bootstrap, false)
}
func (s *Store) createUser(actor, name, password string, admin, bootstrap, local bool) (User, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !validUsername(name) {
		return User{}, errors.New("use 3–64 lowercase letters, digits, dots, underscores or hyphens")
	}
	digest, e := passwordHash(password)
	if e != nil {
		return User{}, e
	}
	tx, e := s.db.Begin()
	if e != nil {
		return User{}, e
	}
	defer tx.Rollback()
	if bootstrap {
		var count int
		if e = tx.QueryRow("SELECT count(*) FROM users").Scan(&count); e != nil {
			return User{}, e
		}
		if count != 0 {
			return User{}, errors.New("first administrator already initialized")
		}
		admin = true
	} else if !local && !isAdmin(tx, actor) {
		return User{}, ErrAccess
	}
	u := User{ID: randomID(), Username: name, Admin: admin, UILocale: "auto"}
	if _, e = tx.Exec("INSERT INTO users(id,username,password_hash,admin) VALUES(?,?,?,?)", u.ID, name, digest, admin); e != nil {
		return User{}, errors.New("unable to create account (username may already exist)")
	}
	return u, tx.Commit()
}
func (s *Store) Login(name, password string) (User, string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	var u User
	var digest string
	err := s.db.QueryRow("SELECT id,username,admin,password_hash,ui_locale FROM users WHERE username=? AND disabled=0", name).Scan(&u.ID, &u.Username, &u.Admin, &digest, &u.UILocale)
	// Compare even unknown accounts at the same bcrypt cost.
	if err != nil {
		digest = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"
	}
	comparison := bcrypt.CompareHashAndPassword([]byte(digest), []byte(password))
	if err != nil || comparison != nil {
		return User{}, "", ErrAccess
	}
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	var valid bool
	if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND disabled=0 AND password_hash=?)", u.ID, digest).Scan(&valid); err != nil || !valid {
		return User{}, "", ErrAccess
	}
	if _, err = tx.Exec("DELETE FROM logins WHERE expires<=unixepoch()"); err != nil {
		return User{}, "", err
	}
	secret := randomID()
	if _, err = tx.Exec("INSERT INTO logins VALUES(?,?,?)", hash(secret), u.ID, time.Now().Add(24*time.Hour).Unix()); err != nil {
		return User{}, "", err
	}
	return u, secret, tx.Commit()
}
func (s *Store) Account(secret string) (User, bool) {
	var u User
	if len(secret) != 64 {
		return u, false
	}
	e := s.db.QueryRow("SELECT u.id,u.username,u.admin,u.disabled,u.ui_locale FROM logins l JOIN users u ON u.id=l.user_id WHERE l.secret_hash=? AND l.expires>unixepoch() AND u.disabled=0", hash(secret)).Scan(&u.ID, &u.Username, &u.Admin, &u.Disabled, &u.UILocale)
	return u, e == nil
}
func (s *Store) Logout(secret string) error {
	_, e := s.db.Exec("DELETE FROM logins WHERE secret_hash=?", hash(secret))
	return e
}
func (s *Store) Users(actor string) ([]User, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if !isAdmin(tx, actor) {
		return nil, ErrAccess
	}
	rows, e := tx.Query("SELECT id,username,admin,disabled FROM users ORDER BY username")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if e = rows.Scan(&u.ID, &u.Username, &u.Admin, &u.Disabled); e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) DisableUser(actor, target string) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if actor == target || !isAdmin(tx, actor) {
		return ErrAccess
	}
	// Other administrators cannot be disabled through this minimal M1 interface.
	res, e := tx.Exec("UPDATE users SET disabled=1 WHERE id=? AND admin=0", target)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrAccess
	}
	if _, e = tx.Exec("DELETE FROM logins WHERE user_id=?", target); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE sessions SET revoked=1 WHERE user_id=?", target); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) ChangePassword(user, old, next string) error {
	var current string
	if e := s.db.QueryRow("SELECT password_hash FROM users WHERE id=? AND disabled=0", user).Scan(&current); e != nil {
		return ErrAccess
	}
	if bcrypt.CompareHashAndPassword([]byte(current), []byte(old)) != nil {
		return ErrAccess
	}
	digest, e := passwordHash(next)
	if e != nil {
		return e
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE users SET password_hash=? WHERE id=? AND disabled=0 AND password_hash=?", digest, user, current)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrAccess
	}
	if _, e = tx.Exec("DELETE FROM logins WHERE user_id=?", user); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) CreateRoom(user, name, mode string) (Room, error) {
	name = strings.TrimSpace(name)
	if mode == "" {
		mode = "meeting"
	}
	if name == "" || len(name) > 100 || (mode != "meeting" && mode != "webinar") {
		return Room{}, errors.New("name (1–100 bytes) and meeting/webinar mode required")
	}
	r := Room{ID: randomRoomID(), Name: name, Mode: mode, OwnerID: user, AccessRole: "owner"}
	res, e := s.db.Exec("INSERT INTO rooms(id,name,mode,owner_id) SELECT ?,?,?,id FROM users WHERE id=? AND disabled=0", r.ID, name, mode, user)
	if e != nil {
		return Room{}, e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return Room{}, ErrAccess
	}
	return r, nil
}
func (s *Store) Rooms(user string) ([]Room, error) {
	rows, e := s.db.Query(`SELECT r.id,r.name,r.mode,COALESCE(r.owner_id,''),r.closed,
 CASE WHEN r.owner_id=u.id THEN 'owner' WHEN EXISTS(SELECT 1 FROM room_admins a WHERE a.room_id=r.id AND a.user_id=u.id) THEN 'admin' ELSE 'shared' END,r.deletion_state
 FROM rooms r JOIN users u ON u.id=? WHERE r.id NOT LIKE 'probe-%' AND u.disabled=0 AND r.deletion_state!='deleted'
 AND ((r.owner_id=u.id) OR (r.closed=0 AND r.deletion_state='' AND NOT EXISTS(SELECT 1 FROM room_bans b WHERE b.room_id=r.id AND b.user_id=u.id) AND
 (EXISTS(SELECT 1 FROM room_admins a WHERE a.room_id=r.id AND a.user_id=u.id) OR EXISTS(SELECT 1 FROM room_memberships m WHERE m.room_id=r.id AND m.user_id=u.id AND m.revoked=0 AND (m.invite_id IS NULL OR EXISTS(SELECT 1 FROM invites i WHERE i.id=m.invite_id AND i.revoked=0 AND i.expires>unixepoch())))))) ORDER BY r.name`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Room{}
	for rows.Next() {
		var r Room
		if e = rows.Scan(&r.ID, &r.Name, &r.Mode, &r.OwnerID, &r.Closed, &r.AccessRole, &r.DeletionState); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) CreateInvite(user, room, role string, hours, maxUses int) (Invitation, string, error) {
	if !validRole(role) || hours < 1 || hours > 720 || maxUses < 1 || maxUses > 1000 {
		return Invitation{}, "", errors.New("role, expiry (1–720 hours) and maxUses (1–1000) required")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return Invitation{}, "", e
	}
	defer tx.Rollback()
	if !canManage(tx, user, room) || (role == "moderator" && !canOwn(tx, user, room)) {
		return Invitation{}, "", ErrAccess
	}
	secret := randomID()
	inv := Invitation{ID: randomID(), Room: room, Role: role, Expires: time.Now().Add(time.Duration(hours) * time.Hour).Unix(), MaxUses: maxUses}
	_, e = tx.Exec("INSERT INTO invites(id,secret_hash,room_id,role,expires,max_uses) SELECT ?,?,?,?,?,? FROM rooms WHERE id=? AND closed=0", inv.ID, hash(secret), room, role, inv.Expires, maxUses, room)
	if e != nil {
		return Invitation{}, "", e
	}
	var count int
	if e = tx.QueryRow("SELECT count(*) FROM invites WHERE id=?", inv.ID).Scan(&count); e != nil || count != 1 {
		return Invitation{}, "", ErrAccess
	}
	return inv, secret, tx.Commit()
}
func (s *Store) Invitations(user, room string) ([]Invitation, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if !canManage(tx, user, room) {
		return nil, ErrAccess
	}
	rows, e := tx.Query("SELECT id,room_id,role,expires,max_uses,uses,revoked FROM invites WHERE room_id=? ORDER BY expires DESC", room)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var i Invitation
		if e = rows.Scan(&i.ID, &i.Room, &i.Role, &i.Expires, &i.MaxUses, &i.Uses, &i.Revoked); e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (s *Store) RevokeInvite(user, room, id string) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if !canOwn(tx, user, room) {
		return ErrAccess
	}
	if _, e = tx.Exec("UPDATE invites SET revoked=1 WHERE id=? AND room_id=?", id, room); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) CloseRoom(user, room string) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if !canOwn(tx, user, room) {
		return ErrAccess
	}
	if _, e = tx.Exec("UPDATE rooms SET closed=1 WHERE id=?", room); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) JoinInvite(code, name string) (Session, string, error) {
	if len(code) < 32 || len(code) > 128 {
		return Session{}, "", ErrAccess
	}
	tx, e := s.db.Begin()
	if e != nil {
		return Session{}, "", e
	}
	defer tx.Rollback()
	var id, room, role, mode string
	var expiry int64
	e = tx.QueryRow("SELECT i.id,i.room_id,i.role,r.mode,i.expires FROM invites i JOIN rooms r ON r.id=i.room_id WHERE i.secret_hash=? AND i.revoked=0 AND i.expires>unixepoch() AND r.closed=0 AND r.locked=0 AND (i.max_uses=0 OR i.uses<i.max_uses)", hash(code)).Scan(&id, &room, &role, &mode, &expiry)
	if e != nil {
		return Session{}, "", ErrAccess
	}
	var count int
	if e = tx.QueryRow("SELECT count(*) FROM sessions WHERE room_id=? AND revoked=0 AND expires>unixepoch()", room).Scan(&count); e != nil {
		return Session{}, "", e
	}
	if count >= 1000 {
		return Session{}, "", errors.New("room session limit reached")
	}
	if _, e = tx.Exec("UPDATE invites SET uses=uses+1 WHERE id=?", id); e != nil {
		return Session{}, "", e
	}
	expiry = min(expiry, time.Now().Add(12*time.Hour).Unix())
	p, secret, e := insertSession(tx, name, room, role, id, "", role != "attendee" || mode == "meeting", expiry)
	if e == nil {
		e = tx.Commit()
	}
	return p, secret, e
}
func (s *Store) JoinRoom(user, room string) (Session, string, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return Session{}, "", e
	}
	defer tx.Rollback()
	if !canManage(tx, user, room) {
		return s.joinShared(tx, user, room)
	}
	var name string
	var closed bool
	if e = tx.QueryRow("SELECT username FROM users WHERE id=?", user).Scan(&name); e != nil {
		return Session{}, "", e
	}
	if e = tx.QueryRow("SELECT closed FROM rooms WHERE id=?", room).Scan(&closed); e != nil || closed {
		return Session{}, "", ErrAccess
	}
	role := "moderator"
	if canOwn(tx, user, room) {
		role = "owner"
	}
	p, secret, e := insertSession(tx, name, room, role, "", user, true, time.Now().Add(12*time.Hour).Unix())
	if e == nil {
		e = tx.Commit()
	}
	return p, secret, e
}
func (s *Store) Moderate(actor, target, action string, publish bool) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	a, e := scanSession(tx.QueryRow(sessionSelect+" AND s.identity=?", actor))
	if e != nil || !a.Moderator {
		return ErrAccess
	}
	p, e := scanSession(tx.QueryRow(sessionSelect+" AND s.identity=?", target))
	if e != nil || p.Room != a.Room || p.Role == "owner" || (p.Moderator && a.Role != "owner") {
		return ErrAccess
	}
	switch action {
	case "admit":
		_, e = tx.Exec("UPDATE sessions SET admitted=1 WHERE identity=?", target)
		if e == nil && p.UserID != "" {
			p.Admitted = true
			e = grantMembership(tx, p)
		}
	case "publish":
		if p.Moderator {
			return ErrAccess
		}
		_, e = tx.Exec("UPDATE sessions SET can_publish=?,role=CASE WHEN ? THEN 'presenter' ELSE 'attendee' END WHERE identity=?", publish, publish, target)
		if e == nil && !publish {
			_, e = tx.Exec("UPDATE rooms SET screen_identity='',screen_until=0 WHERE screen_identity=?", target)
		}
	case "kick":
		if a.Role != "owner" {
			return ErrAccess
		}
		// Anonymous guests can erase cookies. Revoke their invitation as well: identity
		// continuity cannot be enforced for someone holding a still-valid shared link.
		if p.InviteID != "" {
			_, e = tx.Exec("UPDATE invites SET revoked=1 WHERE id=?", p.InviteID)
		}
		if e == nil && p.UserID != "" {
			_, e = tx.Exec("INSERT OR IGNORE INTO room_bans VALUES(?,?)", p.Room, p.UserID)
		}
		if e == nil {
			_, e = tx.Exec("UPDATE sessions SET revoked=1 WHERE identity=?", target)
		}
	default:
		return errors.New("invalid moderation action")
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}
