package observer

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type HistoryStore struct{ db *sql.DB }
type Day struct {
	Date               string   `json:"date"`
	Samples            int      `json:"samples"`
	Expected           int      `json:"expected"`
	CoreAvailability   *float64 `json:"coreAvailability"`
	SignalAvailability *float64 `json:"signalAvailability"`
}
type Incident struct {
	Component string `json:"component"`
	Started   int64  `json:"started"`
	Ended     *int64 `json:"ended"`
}
type History struct {
	Days           []Day      `json:"days"`
	Incidents      []Incident `json:"incidents"`
	SampleInterval int        `json:"sampleIntervalSeconds"`
	Source         string     `json:"source"`
}

func OpenHistory(path string) (*HistoryStore, error) {
	path, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e == nil {
		f.Close()
	} else if !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("observer database must be a private regular file")
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		db.Close()
		return nil, e
	}
	if version > 1 {
		db.Close()
		return nil, errors.New("observer database newer than binary")
	}
	if version == 0 {
		_, e = db.Exec(`BEGIN IMMEDIATE;
 CREATE TABLE samples(at INTEGER PRIMARY KEY,core TEXT NOT NULL,signal TEXT NOT NULL,core_ms INTEGER NOT NULL,signal_ms INTEGER NOT NULL);
 CREATE TABLE incidents(component TEXT NOT NULL,started INTEGER NOT NULL,ended INTEGER,PRIMARY KEY(component,started));
 PRAGMA user_version=1;COMMIT;`)
		if e != nil {
			db.Close()
			return nil, e
		}
	}
	return &HistoryStore{db}, nil
}
func (h *HistoryStore) Close() error { return h.db.Close() }
func (h *HistoryStore) Record(s Snapshot) error {
	at := s.Core.CheckedAt.Unix() / 5 * 5
	tx, e := h.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("INSERT OR REPLACE INTO samples VALUES(?,?,?,?,?)", at, s.Core.State, s.Signaling.State, s.Core.LatencyMS, s.Signaling.LatencyMS); e != nil {
		return e
	}
	for _, v := range []struct{ name, state string }{{"joining", s.Core.State}, {"call-service", s.Signaling.State}} {
		if v.state == "outage" {
			_, e = tx.Exec("INSERT INTO incidents(component,started) SELECT ?,? WHERE NOT EXISTS(SELECT 1 FROM incidents WHERE component=? AND ended IS NULL)", v.name, at, v.name)
		} else if v.state == "operational" {
			_, e = tx.Exec("UPDATE incidents SET ended=? WHERE component=? AND ended IS NULL", at, v.name)
		}
		if e != nil {
			return e
		}
	}
	if _, e = tx.Exec("DELETE FROM samples WHERE at<?", at-30*86400); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM incidents WHERE ended IS NOT NULL AND ended<?", at-30*86400); e != nil {
		return e
	}
	return tx.Commit()
}
func (h *HistoryStore) Read(now time.Time) (History, error) {
	out := History{Days: []Day{}, Incidents: []Incident{}, SampleInterval: 5, Source: "Local server checks; coverage gaps are unknown"}
	start := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -29)
	rows, e := h.db.Query("SELECT strftime('%Y-%m-%d',at,'unixepoch'),count(*),sum(core='operational'),sum(signal='operational'),sum(core IN ('operational','outage')),sum(signal IN ('operational','outage')) FROM samples WHERE at>=? GROUP BY 1 ORDER BY 1", start.Unix())
	if e != nil {
		return out, e
	}
	type counts struct{ n, c, s, ck, sk int }
	byDate := map[string]counts{}
	for rows.Next() {
		var date string
		var v counts
		if e = rows.Scan(&date, &v.n, &v.c, &v.s, &v.ck, &v.sk); e != nil {
			rows.Close()
			return out, e
		}
		byDate[date] = v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for i := 0; i < 30; i++ {
		date := start.AddDate(0, 0, i)
		expected := 17280
		if i == 29 {
			expected = int(now.Sub(date).Seconds())/5 + 1
		}
		v := byDate[date.Format("2006-01-02")]
		d := Day{Date: date.Format("2006-01-02"), Samples: v.n, Expected: expected}
		if v.ck >= 12 {
			pct := float64(v.c) * 100 / float64(v.ck)
			d.CoreAvailability = &pct
		}
		if v.sk >= 12 {
			pct := float64(v.s) * 100 / float64(v.sk)
			d.SignalAvailability = &pct
		}
		out.Days = append(out.Days, d)
	}
	rows, e = h.db.Query("SELECT component,started,ended FROM incidents ORDER BY started DESC LIMIT 100")
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var i Incident
		if e = rows.Scan(&i.Component, &i.Started, &i.Ended); e != nil {
			return out, e
		}
		out.Incidents = append(out.Incidents, i)
	}
	return out, rows.Err()
}
func (h *HistoryStore) Quality() (map[string]any, error) {
	out := map[string]any{"source": "Local HTTP/API probes, not end-to-end media", "windowHours": 24, "media": "unknown"}
	for _, column := range []string{"core_ms", "signal_ms"} {
		var count int
		if e := h.db.QueryRow(fmt.Sprintf("SELECT count(*) FROM samples WHERE at>unixepoch()-86400 AND %s>=0", column)).Scan(&count); e != nil {
			return nil, e
		}
		var p95 *int
		if count >= 12 {
			var n int
			if e := h.db.QueryRow(fmt.Sprintf("SELECT %s FROM samples WHERE at>unixepoch()-86400 AND %s>=0 ORDER BY %s LIMIT 1 OFFSET ?", column, column, column), (count-1)*95/100).Scan(&n); e != nil {
				return nil, e
			}
			p95 = &n
		}
		out[column] = map[string]any{"samples": count, "p95Ms": p95}
	}
	return out, nil
}
