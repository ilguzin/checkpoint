package core

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"strings"
	"time"
	"unicode"
)

const roomAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var roomEncoding = base32.NewEncoding(roomAlphabet).WithPadding(base32.NoPadding)

// Ten random bytes carry 80 bits of entropy. Separators are only for readability.
func randomRoomID() string {
	var value [10]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	raw := roomEncoding.EncodeToString(value[:])
	return raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:]
}

func normalizedRoomID(value string) string {
	var compact strings.Builder
	for _, char := range strings.ToUpper(value) {
		if char == '-' || unicode.IsSpace(char) {
			continue
		}
		switch char {
		case 'O':
			char = '0'
		case 'I', 'L':
			char = '1'
		}
		if !strings.ContainsRune(roomAlphabet, char) {
			return ""
		}
		compact.WriteRune(char)
	}
	raw := compact.String()
	if len(raw) != 16 {
		return ""
	}
	return raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:]
}

// JoinRoomID grants ordinary guest access based on possession of a room ID.
// Existing IDs are matched exactly; only new Crockford IDs are normalized.
func (s *Store) JoinRoomID(code, name string) (Session, string, error) {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	if code == "" || len(code) > 128 || name == "" || len(name) > 80 || strings.HasPrefix(strings.ToLower(code), "probe-") {
		return Session{}, "", ErrAccess
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, "", err
	}
	defer tx.Rollback()
	var room, mode string
	err = tx.QueryRow("SELECT id,mode FROM rooms WHERE id IN (?,?) AND id NOT LIKE 'probe-%' AND closed=0 AND locked=0 ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END LIMIT 1", code, normalizedRoomID(code), code).Scan(&room, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, "", ErrAccess
	}
	if err != nil {
		return Session{}, "", err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM sessions WHERE room_id=? AND revoked=0 AND expires>unixepoch()", room).Scan(&count); err != nil {
		return Session{}, "", err
	}
	if count >= 1000 {
		return Session{}, "", errors.New("room session limit reached")
	}
	p, secret, err := insertSession(tx, name, room, "attendee", "", "", mode == "meeting", time.Now().Add(12*time.Hour).Unix())
	if err == nil {
		err = tx.Commit()
	}
	return p, secret, err
}

// JoinAccess accepts a manually entered room ID or an existing invitation secret.
// A rejected invitation is never converted into elevated room access.
func (s *Store) JoinAccess(code, name string) (Session, string, error) {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return Session{}, "", ErrAccess
	}
	p, secret, err := s.JoinRoomID(code, name)
	if err == nil || !errors.Is(err, ErrAccess) {
		return p, secret, err
	}
	return s.JoinInvite(code, name)
}
