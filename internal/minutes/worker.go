package minutes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	HTTP        *http.Client
	Kind, Token string
}

func NewClient(socket, kind, token string) *Client {
	return &Client{Kind: kind, Token: token, HTTP: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}
func (c *Client) Call(ctx context.Context, path string, in, out any) error {
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	r, e := http.NewRequestWithContext(ctx, "POST", "http://localhost"+path, bytes.NewReader(b))
	if e != nil {
		return e
	}
	r.Header.Set("Authorization", "Bearer "+c.Token)
	r.Header.Set("X-Worker-Kind", c.Kind)
	r.Header.Set("Content-Type", "application/json")
	res, e := c.HTTP.Do(r)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("worker API status %d", res.StatusCode)
	}
	if out == nil {
		_, e = io.Copy(io.Discard, res.Body)
		return e
	}
	return json.NewDecoder(io.LimitReader(res.Body, 12<<20)).Decode(out)
}
func RunWorker(ctx context.Context, kind, configPath, token string) error {
	if kind != "recorder" && kind != "processor" {
		return errors.New("worker must be recorder or processor")
	}
	c, e := LoadConfig(configPath)
	if e != nil {
		return e
	}
	client := NewClient(c.Socket, kind, token)
	var tasks sync.WaitGroup
	var active atomic.Int32
	defer tasks.Wait()
	for ctx.Err() == nil {
		var current Config
		if e := client.Call(ctx, "/configuration", struct{}{}, &current); e != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		ready := current.Enabled
		if kind == "recorder" {
			if _, e = os.Stat(current.FFmpeg); e != nil {
				ready = false
			}
		} else if current.CheckModels() != nil {
			ready = false
		}
		if active.Load() == 0 {
			cleanupWorker(ctx, client, kind)
		}
		capacity := 1
		if kind == "recorder" {
			capacity = current.MaxConcurrentRecordings
		}
		if int(active.Load()) >= capacity {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		var task *Task
		e = client.Call(ctx, "/claim", map[string]bool{"ready": ready}, &task)
		if e == nil && task != nil {
			local := os.Getenv("CHECKPOINT_" + strings.ToUpper(kind) + "_STORAGE")
			if local == "" {
				local = os.Getenv("CHECKPOINT_WORKER_STORAGE")
			}
			if local == "" {
				local = "/var/lib/checkpoint-" + kind
			}
			task.Config.Storage = local
			active.Add(1)
			tasks.Add(1)
			go func(t Task) {
				defer tasks.Done()
				defer active.Add(-1)
				var err error
				if kind == "recorder" {
					err = runRecording(ctx, client, t)
				} else {
					err = runProcessing(ctx, client, t)
				}
				if err != nil && ctx.Err() == nil {
					fmt.Fprintln(os.Stderr, "media task ended without confirmation; core will reconcile its lease")
				}
			}(*task)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

// A heartbeat watchdog cancels child processes before the lease can expire. The
// recorder also owns its confirmation deadline, independent of core availability.
type guard struct {
	mu        sync.Mutex
	directive Directive
	last      time.Time
	phase     string
	started   int64
	reason    string
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	client    *Client
	task      Task
}

func newGuard(ctx context.Context, c *Client, t Task) *guard {
	ctx, cancel := context.WithCancel(ctx)
	g := &guard{ctx: ctx, cancel: cancel, done: make(chan struct{}), client: c, task: t, last: time.Now(), phase: t.Run.State, directive: Directive{Deadline: t.Run.Deadline}}
	go g.loop()
	return g
}
func (g *guard) loop() {
	defer close(g.done)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		g.mu.Lock()
		in := Heartbeat{Run: g.task.Run.ID, Lease: g.task.Lease, Phase: g.phase, Started: g.started, Reason: g.reason, AppliedWindow: g.directive.Window}
		g.mu.Unlock()
		var d Directive
		call, cancel := context.WithTimeout(g.ctx, 5*time.Second)
		e := g.client.Call(call, "/heartbeat", in, &d)
		cancel()
		g.mu.Lock()
		if e == nil {
			g.directive = d
			g.last = time.Now()
		}
		expired := time.Since(g.last) > 12*time.Second
		g.mu.Unlock()
		if expired || d.Cancel {
			g.cancel()
			return
		}
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (g *guard) close()              { g.cancel(); <-g.done }
func (g *guard) snapshot() Directive { g.mu.Lock(); defer g.mu.Unlock(); return g.directive }
func (g *guard) setPhase(phase string, started int64, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.phase = phase
	g.started = started
	g.reason = reason
}
func (g *guard) result(in Result) error {
	in.Run = g.task.Run.ID
	in.Lease = g.task.Lease
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return g.client.Call(ctx, "/result", in, nil)
}
func taskDir(t Task) string { return filepath.Join(t.Config.Storage, t.Run.ID) }

func (c *Client) Upload(ctx context.Context, t Task, path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	req, e := http.NewRequestWithContext(ctx, "PUT", "http://localhost/artifacts/"+t.Run.ID+"/"+filepath.Base(path), f)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Worker-Kind", c.Kind)
	req.Header.Set("X-Task-Lease", t.Lease)
	res, e := c.HTTP.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("artifact upload status %d", res.StatusCode)
	}
	return nil
}
func (c *Client) Download(ctx context.Context, t Task, name, path string) error {
	req, e := http.NewRequestWithContext(ctx, "GET", "http://localhost/artifacts/"+t.Run.ID+"/"+name, nil)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Worker-Kind", c.Kind)
	req.Header.Set("X-Task-Lease", t.Lease)
	res, e := c.HTTP.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("artifact download status %d", res.StatusCode)
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	n, e := io.Copy(f, io.LimitReader(res.Body, (2<<30)+1))
	if n > 2<<30 {
		e = errors.New("artifact exceeds size limit")
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	return e
}

func workerStorage(kind string) string {
	local := os.Getenv("CHECKPOINT_" + strings.ToUpper(kind) + "_STORAGE")
	if local == "" {
		local = os.Getenv("CHECKPOINT_WORKER_STORAGE")
	}
	if local == "" {
		local = "/var/lib/checkpoint-" + kind
	}
	return local
}
func cleanupWorker(ctx context.Context, c *Client, kind string) {
	root := workerStorage(kind)
	entries, e := os.ReadDir(root)
	if e != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 64 {
			continue
		}
		valid := true
		for _, ch := range entry.Name() {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				valid = false
			}
		}
		if !valid {
			continue
		}
		var out struct{ Discard bool }
		if c.Call(ctx, "/discard", map[string]string{"run": entry.Name()}, &out) == nil && out.Discard {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}
