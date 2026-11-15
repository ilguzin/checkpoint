package core

import (
	"errors"
	"golang.org/x/crypto/bcrypt"
	"strings"
)

// Local management methods are for trusted CLI access to the private database.
// They are intentionally not exposed through HTTP routes.
func (s *Store) CreateLocalUser(name, password string, admin bool) (User, error) {
	return s.createUser("", name, password, admin, false, true)
}
func (s *Store) ListLocalUsers() ([]User, error) {
	rows, err := s.db.Query("SELECT id,username,admin,disabled FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var user User
		if err = rows.Scan(&user.ID, &user.Username, &user.Admin, &user.Disabled); err != nil {
			return nil, err
		}
		out = append(out, user)
	}
	return out, rows.Err()
}
func (s *Store) DisableLocalUser(username string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	if err = tx.QueryRow("SELECT id FROM users WHERE username=?", strings.ToLower(strings.TrimSpace(username))).Scan(&id); err != nil {
		return ErrAccess
	}
	if _, err = tx.Exec("UPDATE users SET disabled=1 WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM logins WHERE user_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE sessions SET revoked=1 WHERE user_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListLocalRooms() ([]Room, error) {
	rows, err := s.db.Query("SELECT id,name,mode,COALESCE(owner_id,''),closed FROM rooms WHERE id NOT LIKE 'probe-%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Room{}
	for rows.Next() {
		var room Room
		if err = rows.Scan(&room.ID, &room.Name, &room.Mode, &room.OwnerID, &room.Closed); err != nil {
			return nil, err
		}
		out = append(out, room)
	}
	return out, rows.Err()
}
func (s *Store) CloseLocalRoom(id string) error {
	res, err := s.db.Exec("UPDATE rooms SET closed=1 WHERE id=? OR id=?", strings.TrimSpace(id), normalizedRoomID(id))
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrAccess
	}
	return nil
}

// ResetLocalUserPassword is available only to the trusted local CLI. The explicit
// override applies to this reset, not to the ordinary create/change policy.
func (s *Store) ResetLocalUserPassword(username, password string, allowShort bool) error {
	var digest string
	var err error
	if allowShort {
		if len(password) < 8 || len(password) > 72 {
			return errors.New("explicit local reset requires 8–72 password bytes")
		}
		var hash []byte
		hash, err = bcrypt.GenerateFromPassword([]byte(password), 12)
		digest = string(hash)
	} else {
		digest, err = passwordHash(password)
	}
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	if err = tx.QueryRow("SELECT id FROM users WHERE username=? AND disabled=0", strings.ToLower(strings.TrimSpace(username))).Scan(&id); err != nil {
		return ErrAccess
	}
	if _, err = tx.Exec("UPDATE users SET password_hash=? WHERE id=?", digest, id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM logins WHERE user_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE sessions SET revoked=1 WHERE user_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
