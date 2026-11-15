package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ilguzin/checkpoint/internal/notices"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"google.golang.org/protobuf/proto"
)

type Invite struct {
	Code, Room            string
	Moderator, CanPublish bool
}
type Config struct {
	APIKey, APISecret, LiveKitURL, SignalURL, PublicOrigin, ObserverURL string
	MinutesConfig                                                       string
	Invites                                                             []Invite
}

type RoomService interface {
	ListParticipants(context.Context, *livekit.ListParticipantsRequest) (*livekit.ListParticipantsResponse, error)
	UpdateParticipant(context.Context, *livekit.UpdateParticipantRequest) (*livekit.ParticipantInfo, error)
	RemoveParticipant(context.Context, *livekit.RoomParticipantIdentity) (*livekit.RemoveParticipantResponse, error)
}

type Server struct {
	cfg          Config
	store        *Store
	rooms        RoomService
	permissions  sync.Mutex
	limiter      loginLimiter
	probeLimiter loginLimiter
	probes       chan struct{}
	wake         chan struct{}
	presenceMu   sync.RWMutex
	presence     map[string]map[string]bool
}

func New(cfg Config, store *Store) *Server {
	return &Server{cfg: cfg, store: store, wake: make(chan struct{}, 1), probes: make(chan struct{}, 4), presence: make(map[string]map[string]bool), rooms: lksdk.NewRoomServiceClient(cfg.LiveKitURL, cfg.APIKey, cfg.APISecret)}
}

func (s *Server) token(p Session, publish bool) (string, error) {
	return auth.NewAccessToken(s.cfg.APIKey, s.cfg.APISecret).
		SetIdentity(p.Identity).SetName(p.Name).SetValidFor(10 * time.Minute).
		SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: p.MediaRoom, CanPublish: &publish, CanSubscribe: boolPtr(true), CanPublishData: boolPtr(false), CanUpdateOwnMetadata: boolPtr(false)}).ToJWT()
}
func boolPtr(v bool) *bool { return &v }

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, message string) {
	errorCode := publicErrorCodes[message]
	if errorCode == "" {
		errorCode = "request_failed"
	}
	reply(w, code, map[string]string{"error": message, "code": errorCode})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "Invalid request")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "Invalid request")
		return false
	}
	return true
}

func (s *Server) Handler(ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !s.store.Healthy() {
			fail(w, 503, "Database unavailable")
			return
		}
		reply(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if !s.store.Healthy() {
			fail(w, 503, "Database unavailable")
			return
		}
		if s.store.Maintenance() {
			reply(w, 503, map[string]string{"status": "maintenance"})
			return
		}
		reply(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/licenses", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(notices.Catalog)
	})
	s.managementRoutes(mux)
	s.roomAdminRoutes(mux)
	s.meetingRoutes(mux)
	s.recordingRoutes(mux)
	mux.HandleFunc("POST /api/probe", s.probe)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]string{"observerURL": s.cfg.ObserverURL})
	})
	mux.HandleFunc("POST /api/join", s.join)
	mux.HandleFunc("POST /api/session", s.withSession(s.session))
	mux.HandleFunc("GET /api/participants", s.withSession(s.participants))
	mux.HandleFunc("POST /api/permissions", s.withSession(s.setPermissions))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "Not found") })
	mux.Handle("/", ui)
	return s.headers(mux)
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.cfg.PublicOrigin && origin != s.cfg.ObserverURL {
			fail(w, 403, "Origin not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Invite string `json:"invite"`
		RoomID string `json:"roomId"`
		Access string `json:"access"`
		Name   string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		fail(w, 400, "Enter a name (up to 80 bytes)")
		return
	}
	in.Invite = strings.TrimSpace(in.Invite)
	in.RoomID = strings.TrimSpace(in.RoomID)
	in.Access = strings.TrimSpace(in.Access)
	fields := 0
	for _, value := range []string{in.Invite, in.RoomID, in.Access} {
		if strings.TrimSpace(value) != "" {
			fields++
		}
	}
	if fields != 1 {
		fail(w, 400, "Enter one room ID or invitation")
		return
	}
	var p Session
	var secret string
	var err error
	switch {
	case in.RoomID != "":
		p, secret, err = s.store.JoinRoomID(in.RoomID, in.Name)
	case in.Access != "":
		p, secret, err = s.store.JoinAccess(in.Access, in.Name)
	default:
		p, secret, err = s.store.JoinInvite(strings.TrimSpace(in.Invite), in.Name)
	}
	if err != nil {
		if in.Invite != "" {
			fail(w, 403, "Invitation expired, revoked or fully used")
		} else {
			fail(w, 403, "Room or invitation unavailable. Check the code or ask the host.")
		}
		return
	}
	if u, ok := s.store.Account(accountSecret(r)); ok {
		if r.Header.Get("Origin") != s.cfg.PublicOrigin {
			fail(w, 403, "Same-origin request required")
			return
		}
		p, err = s.store.attachAccount(p, u.ID)
		if err != nil {
			_, _ = s.store.db.Exec("UPDATE sessions SET revoked=1 WHERE secret_hash=?", hash(secret))
			accessError(w, err)
			return
		}
	}
	s.respondSession(w, p, secret)
}

func (s *Server) withSession(next func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.store.Authenticate(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !ok {
			fail(w, 401, "Session expired or access revoked")
			return
		}
		next(w, r, p)
	}
}
func (s *Server) respondSession(w http.ResponseWriter, p Session, secret string) {
	if secret != "" {
		http.SetCookie(w, &http.Cookie{Name: "checkpoint_archive_" + hash(p.Room)[:16], Value: secret, Path: "/api/rooms/" + p.Room + "/", HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicOrigin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: int(time.Until(p.Expires).Seconds())})
	}
	// All initial grants are subscribe-only. The reconciler applies authoritative
	// publication permissions after joining, including after replay of old tokens.
	token := ""
	var err error
	if p.Admitted {
		token, err = s.token(p, false)
	}
	if err != nil {
		fail(w, 500, "Token unavailable")
		return
	}
	var roomName, mode string
	_ = s.store.db.QueryRow("SELECT name,mode FROM rooms WHERE id=?", p.Room).Scan(&roomName, &mode)
	reply(w, 200, map[string]any{"waiting": !p.Admitted, "roomName": roomName, "mode": mode, "token": token, "session": secret, "identity": p.Identity, "name": p.Name, "room": p.Room, "role": p.Role, "moderator": p.Moderator, "canPublish": p.CanPublish, "serverURL": s.cfg.SignalURL})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request, p Session) {
	s.respondSession(w, p, "")
}
func (s *Server) participants(w http.ResponseWriter, r *http.Request, p Session) {
	if !p.Moderator {
		fail(w, 403, "Moderator invitation required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	res, err := s.rooms.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: p.MediaRoom})
	if err != nil {
		fail(w, 503, "Participants unavailable")
		return
	}
	out := []map[string]any{}
	for _, remote := range res.Participants {
		if member, ok := s.store.Get(remote.Identity); ok && member.Room == p.Room {
			out = append(out, map[string]any{"identity": member.Identity, "name": member.Name, "canPublish": member.CanPublish, "moderator": member.Moderator})
		}
	}
	reply(w, 200, out)
}
func permission(publish bool) *livekit.ParticipantPermission {
	return &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: publish, CanPublishData: false, CanUpdateMetadata: false, CanPublishSources: []livekit.TrackSource{livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE}}
}
func sessionPermission(p Session) *livekit.ParticipantPermission {
	v := permission(p.CanPublish && p.Admitted)
	if p.CanScreen && p.CanPublish && p.Admitted {
		v.CanPublishSources = append(v.CanPublishSources, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	}
	return v
}
func (s *Server) setPermissions(w http.ResponseWriter, r *http.Request, p Session) {
	if !p.Moderator {
		fail(w, 403, "Moderator invitation required")
		return
	}
	var in struct {
		Identity   string `json:"identity"`
		CanPublish bool   `json:"canPublish"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.permissions.Lock()
	defer s.permissions.Unlock()
	target, ok := s.store.Get(in.Identity)
	if !ok || target.Room != p.Room || target.Moderator {
		fail(w, 403, "Cannot change this participant")
		return
	}
	if err := s.store.Moderate(p.Identity, in.Identity, "publish", in.CanPublish); err != nil {
		accessError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	_, err := s.rooms.UpdateParticipant(ctx, &livekit.UpdateParticipantRequest{Room: p.MediaRoom, Identity: in.Identity, Permission: permission(in.CanPublish)})
	if err != nil {
		fail(w, 503, "Permission saved; media server confirmation pending")
		return
	}
	reply(w, 200, map[string]bool{"canPublish": in.CanPublish})
}

// SignalHandler exposes only LiveKit's join/validate paths, never its management
// API. Every connection gets a fresh least-privilege grant, even when the SDK
// supplies a refreshed token with previously elevated permissions.
func (s *Server) SignalHandler() http.Handler {
	target, _ := url.Parse(s.cfg.LiveKitURL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) { fail(w, 503, "Media server unavailable") }
	return s.headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.store.Healthy() {
			fail(w, 503, "Session state unavailable")
			return
		}
		if r.Method == "GET" && r.URL.Path == "/healthz" {
			reply(w, 200, map[string]string{"status": "ok"})
			return
		}
		if r.Method != "GET" || (r.URL.Path != "/rtc" && r.URL.Path != "/rtc/validate" && r.URL.Path != "/rtc/v1" && r.URL.Path != "/rtc/v1/validate") {
			fail(w, 404, "Not found")
			return
		}
		if s.store.Maintenance() {
			fail(w, 503, "Maintenance in progress")
			return
		}
		q := r.URL.Query()
		raw := q.Get("access_token")
		if raw == "" {
			raw = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		verifier, err := auth.ParseAPIToken(raw)
		if err != nil || verifier.APIKey() != s.cfg.APIKey {
			fail(w, 401, "Invalid token")
			return
		}
		_, grants, err := verifier.Verify(s.cfg.APISecret)
		if err != nil || grants.Video == nil || !grants.Video.RoomJoin {
			fail(w, 401, "Invalid token")
			return
		}
		p, ok := s.store.Get(verifier.Identity())
		if !ok || !p.Admitted || p.MediaRoom != grants.Video.Room {
			fail(w, 403, "Session unavailable")
			return
		}
		token, err := s.token(p, false)
		if err != nil {
			fail(w, 500, "Token unavailable")
			return
		}
		q.Set("access_token", token)
		r.URL.RawQuery = q.Encode()
		r.Header.Del("Authorization")
		proxy.ServeHTTP(w, r)
	}))
}

func (s *Server) Reconcile(ctx context.Context) {
	_ = s.store.Cleanup()
	s.reconcileOnce(ctx)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Minute)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileOnce(ctx)
		case <-s.wake:
			s.reconcileOnce(ctx)
		case <-cleanup.C:
			_ = s.store.Cleanup()
		}
	}
}
func (s *Server) reconcileOnce(ctx context.Context) {
	s.permissions.Lock()
	defer s.permissions.Unlock()
	rooms, err := s.store.RoomIDs()
	if err != nil {
		return
	}
	for _, room := range rooms {
		func() {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			res, err := s.rooms.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room})
			if err != nil {
				return
			}
			s.presenceMu.Lock()
			connected := map[string]bool{}
			for _, remote := range res.Participants {
				if s.isRecorder(remote.Identity, room) {
					continue
				}
				connected[remote.Identity] = true
			}
			s.presence[room] = connected
			s.presenceMu.Unlock()
			s.recordingPresence(room, connected)
			var screenOwner string
			_ = s.store.db.QueryRow("SELECT screen_identity FROM rooms WHERE (CASE WHEN generation=0 THEN id ELSE id||'--'||generation END)=?", room).Scan(&screenOwner)
			if screenOwner != "" {
				found := false
				for _, remote := range res.Participants {
					if remote.Identity == screenOwner {
						found = true
						break
					}
				}
				if !found {
					_, _ = s.store.db.Exec("UPDATE rooms SET screen_identity='',screen_until=0 WHERE screen_identity=?", screenOwner)
				}
			}
			revocationFailed := false
			for pass := 0; pass < 2; pass++ {
				if pass == 1 && revocationFailed {
					break
				}
				for _, remote := range res.Participants {
					if s.isRecorder(remote.Identity, room) {
						continue
					}
					p, lookupErr := s.store.lookup(remote.Identity)
					if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
						log.Print("session lookup unavailable; reconciliation deferred")
						continue
					}
					if errors.Is(lookupErr, sql.ErrNoRows) || !p.Admitted || p.MediaRoom != room {
						if pass == 1 {
							continue
						}
						if _, e := s.rooms.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{Room: room, Identity: remote.Identity}); e != nil {
							revocationFailed = true
						}
						continue
					}
					if (p.CanScreen && pass == 0) || (!p.CanScreen && pass == 1) {
						continue
					}
					if !proto.Equal(remote.Permission, sessionPermission(p)) {
						if _, err = s.rooms.UpdateParticipant(ctx, &livekit.UpdateParticipantRequest{Room: room, Identity: p.Identity, Permission: sessionPermission(p)}); err != nil {
							revocationFailed = true
							log.Print("permission reconciliation pending")
						}
					}
				}
			}
		}()
	}
}

func ValidateConfig(cfg Config) error {
	if len(cfg.APISecret) < 32 || cfg.APIKey == "" {
		return errors.New("LiveKit credentials are required (secret >= 32 characters)")
	}
	seen := map[string]bool{}
	for _, inv := range cfg.Invites {
		if len(inv.Code) < 32 || seen[inv.Code] {
			return errors.New("distinct test invitations >= 32 characters required")
		}
		seen[inv.Code] = true
	}

	for _, value := range []string{cfg.LiveKitURL, cfg.SignalURL, cfg.PublicOrigin, cfg.ObserverURL} {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") {
			return errors.New("invalid configured URL")
		}
	}
	return nil
}
