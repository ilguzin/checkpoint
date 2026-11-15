package observer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIndependentFailureAndStaleness(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	o := &Observer{coreURL: core.URL, client: &http.Client{Timeout: time.Second}, probe: func(context.Context) error { return nil }}
	if o.Snapshot().Core.State != "unknown" {
		t.Fatal("unmeasured service marked healthy")
	}
	o.Sample(context.Background())
	snap := o.Snapshot()
	if snap.Core.State != "operational" || snap.Signaling.State != "operational" || snap.Media.State != "unknown" {
		t.Fatal(snap)
	}
	core.Close()
	o.Sample(context.Background())
	if o.Snapshot().Core.State != "operational" {
		t.Fatal("single failure changed incident state")
	}
	o.Sample(context.Background())
	snap = o.Snapshot()
	if snap.Core.State != "outage" || snap.Signaling.State != "operational" {
		t.Fatal("core failure not isolated")
	}
	o.probe = func(context.Context) error { return errors.New("SECRET internal address") }
	o.Sample(context.Background())
	w := httptest.NewRecorder()
	o.Handler(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", "/api/health", nil))
	if strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), core.URL) {
		t.Fatal("internal detail exposed")
	}
	o.mu.Lock()
	o.snapshot.Core.CheckedAt = time.Now().Add(-16 * time.Second)
	o.snapshot.Core.State = "operational"
	o.mu.Unlock()
	if o.Snapshot().Core.State != "unknown" {
		t.Fatal("stale check marked healthy")
	}
}

func TestMaintenanceIsExplicitAndUnknown503IsOutage(t *testing.T) {
	body := `{"status":"maintenance"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); w.Write([]byte(body)) }))
	defer server.Close()
	o := &Observer{coreURL: server.URL, client: server.Client(), probe: func(context.Context) error { return nil }}
	o.Sample(context.Background())
	if o.Snapshot().Core.State != "maintenance" {
		t.Fatal("maintenance shown as outage")
	}
	body = `{"status":"failure"}`
	o.Sample(context.Background())
	o.Sample(context.Background())
	if o.Snapshot().Core.State != "outage" {
		t.Fatal("failure shown as maintenance")
	}
}
