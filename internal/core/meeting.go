package core

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Message struct {
	ID       int64  `json:"id"`
	Identity string `json:"identity"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Body     string `json:"body"`
	Created  int64  `json:"created"`
	Answered bool   `json:"answered"`
}
type MeetingState struct {
	Members     []Member  `json:"members"`
	Messages    []Message `json:"messages"`
	Locked      bool      `json:"locked"`
	WaitingRoom bool      `json:"waitingRoom"`
	ScreenOwner string    `json:"screenOwner"`
	Mode        string    `json:"mode"`
	Name        string    `json:"name"`
}
type Member struct {
	Identity   string `json:"identity"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Moderator  bool   `json:"moderator"`
	CanPublish bool   `json:"canPublish"`
	Admitted   bool   `json:"admitted"`
	HandAt     int64  `json:"handAt"`
}

func active(tx *sql.Tx, id string, admitted bool) (Session, error) {
	p, e := scanSession(tx.QueryRow(sessionSelect+" AND s.identity=?", id))
	if e != nil || admitted && !p.Admitted {
		return Session{}, ErrAccess
	}
	return p, nil
}
func (s *Store) MeetingState(identity string) (MeetingState, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return MeetingState{}, e
	}
	defer tx.Rollback()
	p, e := active(tx, identity, true)
	if e != nil {
		return MeetingState{}, e
	}
	out := MeetingState{Members: []Member{}, Messages: []Message{}}
	if e = tx.QueryRow("SELECT locked,waiting_room,CASE WHEN screen_until>unixepoch() THEN screen_identity ELSE '' END,mode,name FROM rooms WHERE id=?", p.Room).Scan(&out.Locked, &out.WaitingRoom, &out.ScreenOwner, &out.Mode, &out.Name); e != nil {
		return out, e
	}
	rows, e := tx.Query(sessionSelect+" AND s.room_id=? ORDER BY s.hand_at DESC,s.name", p.Room)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanSession(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		if v.Admitted || p.Moderator {
			out.Members = append(out.Members, Member{v.Identity, v.Name, v.Role, v.Moderator, v.CanPublish, v.Admitted, v.HandAt})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query("SELECT id,identity,name,kind,body,created,answered FROM (SELECT * FROM messages WHERE room_id=? AND generation=? AND created>unixepoch()-86400 ORDER BY id DESC LIMIT 200) ORDER BY id", p.Room, p.Generation)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var m Message
		if e = rows.Scan(&m.ID, &m.Identity, &m.Name, &m.Kind, &m.Body, &m.Created, &m.Answered); e != nil {
			rows.Close()
			return out, e
		}
		out.Messages = append(out.Messages, m)
	}
	e = rows.Err()
	rows.Close()
	return out, e
}
func (s *Store) Hand(identity string, raised bool) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := active(tx, identity, true)
	if e != nil {
		return e
	}
	// Keep repeated raises in place and give every new raise a distinct queue position.
	_, e = tx.Exec(`UPDATE sessions SET hand_at=CASE WHEN ? THEN
		CASE WHEN hand_at>0 THEN hand_at ELSE max(?,coalesce((SELECT max(hand_at)+1 FROM sessions WHERE room_id=?),0)) END
		ELSE 0 END WHERE identity=?`, raised, time.Now().UnixMilli(), p.Room, identity)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) SendMessage(identity, kind, body string) error {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 2000 || (kind != "chat" && kind != "question") {
		return errors.New("message must contain 1–2000 bytes")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := active(tx, identity, true)
	if e != nil {
		return e
	}
	var count int
	if e = tx.QueryRow("SELECT count(*) FROM messages WHERE identity=? AND created>unixepoch()-10", identity).Scan(&count); e != nil {
		return e
	}
	if count >= 5 {
		return errors.New("message rate exceeded")
	}
	if _, e = tx.Exec("INSERT INTO messages(room_id,generation,identity,name,kind,body,created) VALUES(?,?,?,?,?,?,unixepoch())", p.Room, p.Generation, p.Identity, p.Name, kind, body); e != nil {
		return e
	}
	// Bounded current-session history; there is no permanent archive.
	if _, e = tx.Exec("DELETE FROM messages WHERE room_id=? AND id NOT IN(SELECT id FROM messages WHERE room_id=? ORDER BY id DESC LIMIT 2000)", p.Room, p.Room); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Answer(identity string, message int64) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := active(tx, identity, true)
	if e != nil || !p.Moderator {
		return ErrAccess
	}
	_, e = tx.Exec("UPDATE messages SET answered=1 WHERE id=? AND room_id=? AND generation=? AND kind='question'", message, p.Room, p.Generation)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) MeetingPolicy(identity string, locked, waiting bool) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := active(tx, identity, true)
	if e != nil || p.Role != "owner" {
		return ErrAccess
	}
	if _, e = tx.Exec("UPDATE rooms SET locked=?,waiting_room=? WHERE id=?", locked, waiting, p.Room); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) EndMeeting(identity string) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := active(tx, identity, true)
	if e != nil || p.Role != "owner" {
		return ErrAccess
	}
	if _, e = tx.Exec("UPDATE sessions SET revoked=1,hand_at=0 WHERE room_id=?", p.Room); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM messages WHERE room_id=?", p.Room); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM room_bans WHERE room_id=?", p.Room); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE rooms SET generation=generation+1,locked=0,screen_identity='',screen_until=0 WHERE id=?", p.Room); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Leave(identity string) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("UPDATE sessions SET revoked=1,hand_at=0 WHERE identity=?", identity); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE rooms SET screen_identity='',screen_until=0 WHERE screen_identity=?", identity); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Screen(identity string, claim bool) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := active(tx, identity, true)
	if e != nil {
		return e
	}
	if !claim {
		_, e = tx.Exec("UPDATE rooms SET screen_identity='',screen_until=0 WHERE id=? AND screen_identity=?", p.Room, identity)
	} else {
		if !p.CanPublish {
			return ErrAccess
		}
		res, err := tx.Exec("UPDATE rooms SET screen_identity=?,screen_until=unixepoch()+45 WHERE id=? AND (screen_until<=unixepoch() OR screen_identity=?)", identity, p.Room, identity)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return errors.New("another participant is sharing")
		}
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Cleanup() error {
	_, _ = s.db.Exec("DELETE FROM sessions WHERE room_id LIKE 'probe-%' AND expires<=unixepoch()")
	_, _ = s.db.Exec("DELETE FROM rooms WHERE id LIKE 'probe-%' AND NOT EXISTS(SELECT 1 FROM sessions WHERE room_id=rooms.id)")
	_, e := s.db.Exec("DELETE FROM messages WHERE created<=unixepoch()-86400")
	return e
}
