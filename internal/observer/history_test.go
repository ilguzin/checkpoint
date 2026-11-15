package observer

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryPersistsGapsIncidentsAndSampleThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observer.db")
	h, e := OpenHistory(path)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	base := now.Add(-time.Minute)
	for i := 0; i < 12; i++ {
		state := "operational"
		if i >= 4 && i < 8 {
			state = "outage"
		}
		stamp := base.Add(time.Duration(i*5) * time.Second)
		if e = h.Record(Snapshot{Core: Check{State: state, CheckedAt: stamp, LatencyMS: 2}, Signaling: Check{State: "operational", CheckedAt: stamp, LatencyMS: 3}}); e != nil {
			t.Fatal(e)
		}
	}
	h.Close()
	h, e = OpenHistory(path)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	out, e := h.Read(now)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Days) != 30 || len(out.Incidents) != 1 || out.Incidents[0].Ended == nil {
		t.Fatal("history/incident not retained")
	}
	if out.Days[0].Samples != 0 || out.Days[0].CoreAvailability != nil {
		t.Fatal("gap reported healthy")
	}
	var total int
	for _, d := range out.Days {
		total += d.Samples
	}
	if total != 12 {
		t.Fatal("samples lost")
	}
	q, e := h.Quality()
	if e != nil || q["media"] != "unknown" {
		t.Fatal("invented media data", e)
	}
}
