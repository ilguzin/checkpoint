package core

import (
	"database/sql"
	"errors"
	"time"
)

type RoomAdmin struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Disabled bool   `json:"disabled"`
}
type RoomAdminInvitation struct {
	ID      string `json:"id"`
	Room    string `json:"room"`
	Expires int64  `json:"expires"`
}

func (s *Store) CreateRoomAdminInvite(actor, room string) (RoomAdminInvitation, string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return RoomAdminInvitation{}, "", err
	}
	defer tx.Rollback()
	if !canOwn(tx, actor, room) {
		return RoomAdminInvitation{}, "", ErrAccess
	}
	invite := RoomAdminInvitation{ID: randomID(), Room: room, Expires: time.Now().Add(24 * time.Hour).Unix()}
	secret := randomID()
	res, err := tx.Exec("INSERT INTO room_admin_invites(id,secret_hash,room_id,expires) SELECT ?,?,?,? FROM rooms WHERE id=? AND closed=0", invite.ID, hash(secret), room, invite.Expires, room)
	if err != nil {
		return RoomAdminInvitation{}, "", err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return RoomAdminInvitation{}, "", err
	}
	if count != 1 {
		return RoomAdminInvitation{}, "", ErrAccess
	}
	return invite, secret, tx.Commit()
}
func (s *Store) AcceptRoomAdminInvite(user, secret string) (Room, error) {
	if len(secret) != 64 {
		return Room{}, ErrAccess
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Room{}, err
	}
	defer tx.Rollback()
	var enabled bool
	if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND disabled=0)", user).Scan(&enabled); err != nil {
		return Room{}, err
	}
	if !enabled {
		return Room{}, ErrAccess
	}
	var out Room
	var inviteID string
	err = tx.QueryRow("SELECT r.id,r.name,r.mode,COALESCE(r.owner_id,''),r.closed,i.id FROM room_admin_invites i JOIN rooms r ON r.id=i.room_id WHERE i.secret_hash=? AND i.expires>unixepoch() AND i.revoked=0 AND i.accepted_by IS NULL AND r.closed=0", hash(secret)).Scan(&out.ID, &out.Name, &out.Mode, &out.OwnerID, &out.Closed, &inviteID)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrAccess
	}
	if err != nil {
		return Room{}, err
	}
	if out.OwnerID == user {
		return Room{}, ErrAccess
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO room_admins(room_id,user_id) VALUES(?,?)", out.ID, user); err != nil {
		return Room{}, err
	}
	if _, err = tx.Exec("UPDATE room_admin_invites SET accepted_by=? WHERE id=?", user, inviteID); err != nil {
		return Room{}, err
	}
	out.AccessRole = "admin"
	return out, tx.Commit()
}
func (s *Store) RoomAdmins(actor, room string) ([]RoomAdmin, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if !canOwn(tx, actor, room) {
		return nil, ErrAccess
	}
	rows, err := tx.Query("SELECT u.id,u.username,u.disabled FROM room_admins a JOIN users u ON u.id=a.user_id WHERE a.room_id=? ORDER BY u.username", room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoomAdmin{}
	for rows.Next() {
		var member RoomAdmin
		if err = rows.Scan(&member.ID, &member.Username, &member.Disabled); err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}
func (s *Store) RemoveRoomAdmin(actor, room, target string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !canOwn(tx, actor, room) {
		return ErrAccess
	}
	if _, err = tx.Exec("DELETE FROM room_admins WHERE room_id=? AND user_id=?", room, target); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE sessions SET revoked=1 WHERE room_id=? AND user_id=? AND role='moderator'", room, target); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RevokeRoomAdminInvite(actor, room, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !canOwn(tx, actor, room) {
		return ErrAccess
	}
	if _, err = tx.Exec("UPDATE room_admin_invites SET revoked=1 WHERE room_id=? AND id=?", room, id); err != nil {
		return err
	}
	return tx.Commit()
}
