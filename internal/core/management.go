package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
	"google.golang.org/protobuf/encoding/protojson"
)

// Deliberately use the connection peer, not untrusted forwarded headers. Behind
// an edge this is a conservative shared budget; the edge may add per-client limits.
type rateBucket struct {
	start time.Time
	count int
}
type loginLimiter struct {
	mu      sync.Mutex
	clients map[string]rateBucket
	global  rateBucket
}

func (l *loginLimiter) allow(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.clients == nil {
		l.clients = map[string]rateBucket{}
	}
	for k, b := range l.clients {
		if now.Sub(b.start) > time.Minute {
			delete(l.clients, k)
		}
	}
	if now.Sub(l.global.start) > time.Minute {
		l.global = rateBucket{start: now}
	}
	b := l.clients[host]
	if b.start.IsZero() {
		b.start = now
	}
	if b.count >= 10 || l.global.count >= 60 || len(l.clients) >= 4096 {
		return false
	}
	b.count++
	l.global.count++
	l.clients[host] = b
	return true
}

const loginCookie = "checkpoint_account"

func (s *Server) cookie(w http.ResponseWriter, secret string) {
	age := 86400
	if secret == "" {
		age = -1
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: secret, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicOrigin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func accountSecret(r *http.Request) string {
	c, e := r.Cookie(loginCookie)
	if e != nil {
		return ""
	}
	return c.Value
}
func (s *Server) withAccount(next func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Header.Get("Origin") != s.cfg.PublicOrigin {
			fail(w, 403, "Same-origin request required")
			return
		}
		u, ok := s.store.Account(accountSecret(r))
		if !ok {
			fail(w, 401, "Sign in required")
			return
		}
		next(w, r, u)
	}
}
func accessError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrAccess) {
		fail(w, 403, "Access unavailable")
	} else {
		fail(w, 400, "Unable to apply this change; check its values")
	}
}
func (s *Server) managementRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/account/login", s.login)
	mux.HandleFunc("GET /api/account", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) { reply(w, 200, u) }))
	mux.HandleFunc("POST /api/account/preferences", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		var in struct {
			UILocale string `json:"uiLocale"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.UILocale != "auto" && in.UILocale != "en" && in.UILocale != "ru" {
			fail(w, 400, "Invalid interface language")
			return
		}
		if _, err := s.store.db.Exec("UPDATE users SET ui_locale=? WHERE id=? AND disabled=0", in.UILocale, u.ID); err != nil {
			fail(w, 503, "Unable to save preferences")
			return
		}
		u.UILocale = in.UILocale
		reply(w, 200, u)
	}))

	mux.HandleFunc("POST /api/account/logout", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		if e := s.store.Logout(accountSecret(r)); e != nil {
			fail(w, 503, "Unable to sign out")
			return
		}
		s.cookie(w, "")
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/account/password", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		if !s.limiter.allow(r) {
			fail(w, 429, "Try again in a minute")
			return
		}
		var in struct{ Current, Password string }
		if !decode(w, r, &in) {
			return
		}
		if e := s.store.ChangePassword(u.ID, in.Current, in.Password); e != nil {
			accessError(w, e)
			return
		}
		s.cookie(w, "")
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/rooms", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		out, e := s.store.Rooms(u.ID)
		if e != nil {
			accessError(w, e)
			return
		}
		reply(w, 200, out)
	}))
	mux.HandleFunc("POST /api/rooms", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		var in struct{ Name, Mode string }
		if !decode(w, r, &in) {
			return
		}
		out, e := s.store.CreateRoom(u.ID, in.Name, in.Mode)
		if e != nil {
			accessError(w, e)
			return
		}
		reply(w, 201, out)
	}))
	mux.HandleFunc("POST /api/rooms/{room}/join", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		p, secret, e := s.store.JoinRoom(u.ID, r.PathValue("room"))
		if e != nil {
			accessError(w, e)
			return
		}
		s.respondSession(w, p, secret)
	}))
	mux.HandleFunc("POST /api/rooms/{room}/close", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		s.permissions.Lock()
		defer s.permissions.Unlock()
		if e := s.store.CloseRoom(u.ID, r.PathValue("room")); e != nil {
			accessError(w, e)
			return
		}
		s.wakeReconcile()
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/rooms/{room}/invitations", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		out, e := s.store.Invitations(u.ID, r.PathValue("room"))
		if e != nil {
			accessError(w, e)
			return
		}
		reply(w, 200, out)
	}))
	mux.HandleFunc("POST /api/rooms/{room}/invitations", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		var in struct {
			Role           string
			Hours, MaxUses int
		}
		if !decode(w, r, &in) {
			return
		}
		out, code, e := s.store.CreateInvite(u.ID, r.PathValue("room"), in.Role, in.Hours, in.MaxUses)
		if e != nil {
			accessError(w, e)
			return
		}
		reply(w, 201, map[string]any{"invitation": out, "code": code, "url": s.cfg.PublicOrigin + "/#invite=" + code})
	}))
	mux.HandleFunc("POST /api/rooms/{room}/invitations/{id}/revoke", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		s.permissions.Lock()
		defer s.permissions.Unlock()
		if e := s.store.RevokeInvite(u.ID, r.PathValue("room"), r.PathValue("id")); e != nil {
			accessError(w, e)
			return
		}
		s.wakeReconcile()
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/kick", s.withSession(s.kick))
	mux.HandleFunc("POST /api/livekit/webhook", s.receiveWebhook)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.cfg.PublicOrigin {
		fail(w, 403, "Same-origin request required")
		return
	}
	if !s.limiter.allow(r) {
		fail(w, 429, "Try again in a minute")
		return
	}
	var in struct{ Username, Password string }
	if !decode(w, r, &in) {
		return
	}
	if len(in.Password) > 72 {
		fail(w, 401, "Invalid credentials")
		return
	}
	u, secret, e := s.store.Login(in.Username, in.Password)
	if e != nil {
		fail(w, 401, "Invalid credentials")
		return
	}
	s.cookie(w, secret)
	reply(w, 200, u)
}
func (s *Server) kick(w http.ResponseWriter, r *http.Request, p Session) {
	var in struct{ Identity string }
	if !decode(w, r, &in) {
		return
	}
	s.permissions.Lock()
	defer s.permissions.Unlock()
	if e := s.store.Moderate(p.Identity, in.Identity, "kick", false); e != nil {
		accessError(w, e)
		return
	}
	s.wakeReconcile()
	reply(w, 200, map[string]bool{"revoked": true})
}
func (s *Server) wakeReconcile() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Server) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := webhook.Receive(r, auth.NewSimpleKeyProvider(s.cfg.APIKey, s.cfg.APISecret))
	if err != nil {
		fail(w, 401, "Invalid webhook")
		return
	}
	var event livekit.WebhookEvent
	if err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, &event); err != nil || event.Id == "" || len(event.Id) > 128 {
		fail(w, 400, "Invalid event")
		return
	}
	digest := sha256.Sum256(body)
	sum := hex.EncodeToString(digest[:])
	tx, err := s.store.db.Begin()
	if err != nil {
		fail(w, 503, "Event store unavailable")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM webhook_events WHERE received<unixepoch()-604800"); err != nil {
		fail(w, 503, "Event store unavailable")
		return
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM webhook_events").Scan(&count); err != nil || count >= 100000 {
		fail(w, 503, "Event store full")
		return
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO webhook_events VALUES(?,?,unixepoch())", event.Id, sum); err != nil {
		fail(w, 503, "Event store unavailable")
		return
	}
	var existing string
	if err = tx.QueryRow("SELECT digest FROM webhook_events WHERE id=?", event.Id).Scan(&existing); err != nil {
		fail(w, 503, "Event store unavailable")
		return
	}
	if existing != sum {
		fail(w, 409, "Conflicting event ID")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 503, "Event store unavailable")
		return
	}
	// Events are hints, never permission updates: reordered/replayed events cannot
	// resurrect a ban or demotion. Reconciliation always reads authoritative state.
	s.wakeReconcile()
	reply(w, 200, map[string]bool{"ok": true})
}
