package core

import "database/sql"

func maintenance(tx *sql.Tx) bool {
	var count int
	return tx.QueryRow("SELECT count(*) FROM metadata WHERE key='maintenance' AND value='1'").Scan(&count) != nil || count != 0
}
func (s *Store) Maintenance() bool {
	var count int
	return s.db.QueryRow("SELECT count(*) FROM metadata WHERE key='maintenance' AND value='1'").Scan(&count) != nil || count != 0
}
func (s *Store) SetMaintenance(on bool) error {
	value := "0"
	if on {
		value = "1"
	}
	_, e := s.db.Exec("INSERT INTO metadata(key,value) VALUES('maintenance',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", value)
	return e
}
