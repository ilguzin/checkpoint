package core

// Snapshot produces a consistent standalone SQLite image while short writes may
// continue. The caller creates the empty destination with private permissions.
func (s *Store) Snapshot(path string) error { _, err := s.db.Exec("VACUUM INTO ?", path); return err }
