package observer

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type Check struct {
	State     string    `json:"state"`
	CheckedAt time.Time `json:"checkedAt"`
	Source    string    `json:"source"`
	LatencyMS int64     `json:"latencyMs"`
}
type Snapshot struct {
	Core            Check     `json:"core"`
	Signaling       Check     `json:"signaling"`
	Media           Check     `json:"media"`
	Now             time.Time `json:"now"`
	Recording       Check     `json:"recording"`
	Processing      Check     `json:"processing"`
	ProcessingQueue int       `json:"processingQueue"`
}
type Observer struct {
	mu        sync.RWMutex
	snapshot  Snapshot
	coreURL   string
	client    *http.Client
	probe     func(context.Context) error
	history   *HistoryStore
	failures  [2]int
	successes [2]int
	states    [2]string
}

func New(coreURL, livekitURL, key, secret string) *Observer {
	rooms := lksdk.NewRoomServiceClient(livekitURL, key, secret)
	return &Observer{coreURL: coreURL, client: &http.Client{Timeout: 2 * time.Second}, probe: func(ctx context.Context) error {
		_, err := rooms.ListRooms(ctx, &livekit.ListRoomsRequest{Names: []string{"checkpoint-observer-probe"}})
		return err
	}}
}
func (o *Observer) Sample(ctx context.Context) {
	var snap Snapshot
	snap.Core = Check{State: "outage", Source: "Local HTTP readiness", LatencyMS: -1}
	snap.Signaling = Check{State: "outage", Source: "Local authenticated LiveKit API", LatencyMS: -1}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, "GET", o.coreURL, nil)
		if err == nil {
			res, e := o.client.Do(req)
			if e == nil {
				if res.StatusCode == 503 {
					var body struct {
						Status string `json:"status"`
					}
					if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body) == nil && body.Status == "maintenance" {
						snap.Core.State = "maintenance"
					}
				}
				res.Body.Close()
				if res.StatusCode == 200 {
					snap.Core.State = "operational"
					snap.Core.LatencyMS = time.Since(started).Milliseconds()
				}
			}
		}
		snap.Core.CheckedAt = time.Now().UTC()
	}()
	go func() {
		defer wg.Done()
		started := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if o.probe(probeCtx) == nil {
			snap.Signaling.State = "operational"
			snap.Signaling.LatencyMS = time.Since(started).Milliseconds()
		}
		snap.Signaling.CheckedAt = time.Now().UTC()
	}()
	wg.Wait()
	snap.Recording = Check{State: "unknown", Source: "Recording worker heartbeat", CheckedAt: time.Now().UTC()}
	snap.Processing = Check{State: "unknown", Source: "Minutes worker heartbeat", CheckedAt: time.Now().UTC()}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(o.coreURL, "/healthz")+"/api/media-health", nil)
	if err == nil {
		if response, e := o.client.Do(req); e == nil {
			var body struct {
				Enabled                    bool
				State, Recorder, Processor string
				Queue                      int
			}
			if response.StatusCode == 200 && json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body) == nil {
				if !body.Enabled {
					snap.Recording.State = "disabled"
					snap.Processing.State = "disabled"
				} else {
					snap.Recording.State = "degraded"
					snap.Processing.State = "degraded"
					if body.Recorder == "ready" {
						snap.Recording.State = "operational"
					}
					if body.Processor == "ready" {
						snap.Processing.State = "operational"
					}
					snap.ProcessingQueue = body.Queue
				}
			}
			response.Body.Close()
		}
	}
	snap.Media = Check{State: "unknown", Source: "No continuous end-to-end media measurement"}
	o.mu.Lock()
	for idx, check := range []*Check{&snap.Core, &snap.Signaling} {
		if check.State == "maintenance" {
			o.states[idx] = "maintenance"
			o.failures[idx], o.successes[idx] = 0, 0
		} else if check.State == "outage" {
			o.failures[idx]++
			o.successes[idx] = 0
			if o.failures[idx] >= 2 {
				o.states[idx] = "outage"
			}
		} else {
			o.successes[idx]++
			o.failures[idx] = 0
			if o.states[idx] == "" || o.successes[idx] >= 2 {
				o.states[idx] = "operational"
			}
		}
		if o.states[idx] == "" {
			check.State = "unknown"
		} else {
			check.State = o.states[idx]
		}
	}
	o.snapshot = snap
	o.mu.Unlock()
	if o.history != nil {
		if e := o.history.Record(snap); e != nil {
			log.Print("observer history write failed")
		}
	}
}
func fresh(c Check, now time.Time) Check {
	if c.CheckedAt.IsZero() || now.Sub(c.CheckedAt) > 15*time.Second {
		c.State = "unknown"
	}
	return c
}
func (o *Observer) Snapshot() Snapshot {
	o.mu.RLock()
	snap := o.snapshot
	o.mu.RUnlock()
	snap.Now = time.Now().UTC()
	snap.Core = fresh(snap.Core, snap.Now)
	snap.Signaling = fresh(snap.Signaling, snap.Now)
	snap.Media = fresh(snap.Media, snap.Now)
	snap.Recording = fresh(snap.Recording, snap.Now)
	snap.Processing = fresh(snap.Processing, snap.Now)
	return snap
}
func (o *Observer) Run(ctx context.Context) {
	o.Sample(ctx)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			o.Sample(ctx)
		}
	}
}
func (o *Observer) Handler(ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if o.history == nil {
			http.Error(w, "History unavailable", 503)
			return
		}
		out, e := o.history.Read(time.Now().UTC())
		if e != nil {
			http.Error(w, "History unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /api/quality", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if o.history == nil {
			http.Error(w, "Measurements unavailable", 503)
			return
		}
		out, e := o.history.Quality()
		if e != nil {
			http.Error(w, "Measurements unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/probe", func(w http.ResponseWriter, r *http.Request) {
		// Fixed loopback/upstream endpoint. No caller-supplied URL or request body.
		req, e := http.NewRequestWithContext(r.Context(), "POST", strings.TrimSuffix(o.coreURL, "/healthz")+"/api/probe", nil)
		if e != nil {
			http.Error(w, "Check unavailable", 503)
			return
		}
		client := http.Client{Timeout: 15 * time.Second}
		res, e := client.Do(req)
		if e != nil {
			http.Error(w, "Check unavailable", 503)
			return
		}
		defer res.Body.Close()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(res.Body, 16384))
	})
	mux.HandleFunc("GET /api/check", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(o.Snapshot())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.Handle("/", ui)
	return mux
}

func (o *Observer) AttachHistory(h *HistoryStore) { o.history = h }
