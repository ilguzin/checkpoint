package core

import (
	"database/sql"
	"net/http"
	"strings"
	"time"
)

// Legacy grants retain their original revocation/expiry boundary. A later
// admission using the room secret can explicitly upgrade a bounded membership.
func grantMembership(tx *sql.Tx, p Session) error {
	if p.UserID == "" || !p.Admitted {
		return nil
	}
	_, e := tx.Exec(`INSERT INTO room_memberships(room_id,user_id,invite_id) VALUES(?,?,NULLIF(?,'')) ON CONFLICT(room_id,user_id) DO UPDATE SET revoked=0,invite_id=CASE WHEN room_memberships.invite_id IS NULL THEN NULL ELSE excluded.invite_id END`, p.Room, p.UserID, p.InviteID)
	return e
}
func (s *Store) attachAccount(p Session, user string) (Session, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return p, e
	}
	defer tx.Rollback()
	var banned bool
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM room_bans WHERE room_id=? AND user_id=?)", p.Room, user).Scan(&banned); e != nil || banned {
		return p, ErrAccess
	}
	// An authenticated guest never inherits a legacy invitation's moderator role.
	role := p.Role
	if role == "moderator" {
		role = "attendee"
	}
	if canOwn(tx, user, p.Room) {
		role = "owner"
	} else if canManage(tx, user, p.Room) {
		role = "moderator"
	}
	_, e = tx.Exec("UPDATE sessions SET user_id=?,role=? WHERE identity=? AND revoked=0", user, role, p.Identity)
	if e != nil {
		return p, e
	}
	p.UserID = user
	p.Role = role
	p.Moderator = role == "owner" || role == "moderator"
	if p.Moderator {
		p.Admitted = true
		p.CanPublish = true
		if _, e = tx.Exec("UPDATE sessions SET admitted=1,can_publish=1 WHERE identity=?", p.Identity); e != nil {
			return p, e
		}
	}
	if e = grantMembership(tx, p); e != nil {
		return p, e
	}
	return p, tx.Commit()
}
func canReadHistory(tx *sql.Tx, user, room string) bool {
	var ok bool
	e := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM rooms r JOIN users u ON u.id=? WHERE r.id=? AND r.deletion_state='' AND u.disabled=0 AND
 (r.owner_id=u.id OR (r.closed=0 AND NOT EXISTS(SELECT 1 FROM room_bans b WHERE b.room_id=r.id AND b.user_id=u.id) AND
 (EXISTS(SELECT 1 FROM room_admins a WHERE a.room_id=r.id AND a.user_id=u.id) OR EXISTS(SELECT 1 FROM room_memberships m WHERE m.room_id=r.id AND m.user_id=u.id AND m.revoked=0 AND (m.invite_id IS NULL OR EXISTS(SELECT 1 FROM invites i WHERE i.id=m.invite_id AND i.revoked=0 AND i.expires>unixepoch())))))))`, user, room).Scan(&ok)
	return e == nil && ok
}
func (s *Store) joinShared(tx *sql.Tx, user, room string) (Session, string, error) {
	if !canReadHistory(tx, user, room) {
		return Session{}, "", ErrAccess
	}
	var name, mode string
	var locked, closed bool
	if e := tx.QueryRow("SELECT u.username,r.mode,r.locked,r.closed FROM users u,rooms r WHERE u.id=? AND r.id=?", user, room).Scan(&name, &mode, &locked, &closed); e != nil || locked || closed {
		return Session{}, "", ErrAccess
	}
	var invite string
	if e := tx.QueryRow("SELECT COALESCE(invite_id,'') FROM room_memberships WHERE room_id=? AND user_id=? AND revoked=0", room, user).Scan(&invite); e != nil {
		return Session{}, "", ErrAccess
	}
	expiry := time.Now().Add(12 * time.Hour).Unix()
	if invite != "" {
		var limit int64
		if e := tx.QueryRow("SELECT expires FROM invites WHERE id=? AND revoked=0", invite).Scan(&limit); e != nil {
			return Session{}, "", ErrAccess
		}
		expiry = min(expiry, limit)
	}
	p, secret, e := insertSession(tx, name, room, "attendee", invite, user, mode == "meeting", expiry)
	if e == nil {
		e = tx.Commit()
	}
	return p, secret, e
}

// A bearer session is required for anonymous archives; knowledge of a room ID
// alone never bypasses waiting-room admission. Cookie-only mutations require CSRF origin.
func (s *Server) archiveActor(r *http.Request, tx *sql.Tx, room string) (actor string, manage, owner bool, err error) {
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if secret == "" && r.Method == "GET" && accountSecret(r) == "" {
		if c, e := r.Cookie("checkpoint_archive_" + hash(room)[:16]); e == nil {
			secret = c.Value
		}
	}
	if secret != "" {
		p, e := scanSession(tx.QueryRow(sessionSelect+" AND s.secret_hash=?", hash(secret)))
		if e != nil || p.Room != room || !p.Admitted {
			return "", false, false, ErrAccess
		}
		return p.Identity, p.Moderator, p.Role == "owner", nil
	}
	if r.Method != "GET" && r.Header.Get("Origin") != s.cfg.PublicOrigin {
		return "", false, false, ErrAccess
	}
	var user string
	if e := tx.QueryRow("SELECT u.id FROM logins l JOIN users u ON u.id=l.user_id WHERE l.secret_hash=? AND l.expires>unixepoch() AND u.disabled=0", hash(accountSecret(r))).Scan(&user); e != nil || !canReadHistory(tx, user, room) {
		return "", false, false, ErrAccess
	}
	return user, canManage(tx, user, room), canOwn(tx, user, room), nil
}
