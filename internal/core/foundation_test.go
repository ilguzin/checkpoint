package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

func admin(t *testing.T, s *Store) User {
	t.Helper()
	u, e := s.CreateUser("", "admin", "correct horse battery staple", true, true)
	if e != nil {
		t.Fatal(e)
	}
	return u
}
func mustRoom(t *testing.T, s *Store, u User, mode string) Room {
	t.Helper()
	r, e := s.CreateRoom(u.ID, "Team", mode)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func mustInvite(t *testing.T, s *Store, u User, r Room, role string, uses int) (Invitation, string) {
	t.Helper()
	i, c, e := s.CreateInvite(u.ID, r.ID, role, 24, uses)
	if e != nil {
		t.Fatal(e)
	}
	return i, c
}
func accountRequest(h http.Handler, method, path, secret, body, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	if secret != "" {
		r.AddCookie(&http.Cookie{Name: loginCookie, Value: secret})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAccountsCSRFAndRevocation(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	h := s.Handler(http.NotFoundHandler())
	if _, e := s.store.CreateUser("", "another", "correct horse battery staple", true, true); e == nil {
		t.Fatal("bootstrap repeated")
	}
	body := `{"username":"admin","password":"correct horse battery staple"}`
	for _, origin := range []string{"", "https://evil.example"} {
		if w := accountRequest(h, "POST", "/api/account/login", "", body, origin); w.Code != 403 {
			t.Fatal("login CSRF accepted")
		}
	}
	w := accountRequest(h, "POST", "/api/account/login", "", body, s.cfg.PublicOrigin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	secret := cookies[0].Value
	if w = accountRequest(h, "POST", "/api/rooms", secret, `{"name":"Team","mode":"webinar"}`, ""); w.Code != 403 {
		t.Fatal("mutation CSRF accepted")
	}
	other, e := s.store.CreateUser(u.ID, "organizer", "another long password", false, false)
	if e != nil {
		t.Fatal(e)
	}
	room := mustRoom(t, s.store, other, "meeting")
	_, login, e := s.store.Login(other.Username, "another long password")
	if e != nil {
		t.Fatal(e)
	}
	p, ps, e := s.store.JoinRoom(other.ID, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.store.DisableUser(u.ID, other.ID); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.store.Account(login); ok {
		t.Fatal("disabled account login alive")
	}
	if _, ok := s.store.Authenticate(ps); ok {
		t.Fatal("disabled account media alive")
	}
	if _, ok := s.store.Get(p.Identity); ok {
		t.Fatal("disabled identity alive")
	}
	if _, _, e = s.store.JoinRoom(other.ID, room.ID); e == nil {
		t.Fatal("disabled owner rejoined")
	}
	if w = accountRequest(h, "POST", "/api/account/logout", secret, "{}", s.cfg.PublicOrigin); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if _, ok := s.store.Account(secret); ok {
		t.Fatal("logout ineffective")
	}
}
func TestInviteConcurrentUseAndReadOnlySignal(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "webinar")
	inv, code := mustInvite(t, s.store, u, r, "attendee", 1)
	other, e := OpenStore(s.store.path)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var wins atomic.Int32
	var p Session
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s.store
			if i%2 == 0 {
				db = other
			}
			v, _, e := db.JoinInvite(code, "Guest")
			if e == nil {
				wins.Add(1)
				mu.Lock()
				p = v
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("one-use invitation admitted %d", wins.Load())
	}
	if p.CanPublish {
		t.Fatal("viewer can publish")
	}
	ro, e := OpenReadOnlyStore(s.store.path)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	if _, ok := ro.Get(p.Identity); !ok {
		t.Fatal("signal did not see committed session")
	}
	if _, e = ro.db.Exec("DELETE FROM sessions"); e == nil {
		t.Fatal("read-only DB writable")
	}
	token, _ := s.token(p, true)
	signal := New(s.cfg, ro).SignalHandler()
	if e = s.store.RevokeInvite(u.ID, r.ID, inv.ID); e != nil {
		t.Fatal(e)
	}
	if _, ok := ro.Get(p.Identity); ok {
		t.Fatal("signal cached revoked session")
	}
	if w := request(signal, "GET", "/rtc?access_token="+token, "", ""); w.Code != 403 {
		t.Fatalf("old token accepted: %d", w.Code)
	}
	if _, _, e = s.store.JoinInvite(code, "Again"); e == nil {
		t.Fatal("revoked invitation reusable")
	}
}
func TestRoomIsolationKickAndExpiry(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "webinar")
	other := mustRoom(t, s.store, u, "meeting")
	host, _, _ := s.store.JoinRoom(u.ID, r.ID)
	_, code := mustInvite(t, s.store, u, r, "attendee", 5)
	guest, secret, e := s.store.JoinInvite(code, "Guest")
	if e != nil {
		t.Fatal(e)
	}
	_, foreignCode := mustInvite(t, s.store, u, other, "attendee", 1)
	foreign, _, _ := s.store.JoinInvite(foreignCode, "Other")
	if e = s.store.Moderate(host.Identity, foreign.Identity, "publish", true); e == nil {
		t.Fatal("cross-room privilege escalation")
	}
	if e = s.store.Moderate(guest.Identity, guest.Identity, "publish", true); e == nil {
		t.Fatal("self elevation")
	}
	if e = s.store.Moderate(host.Identity, guest.Identity, "publish", true); e != nil {
		t.Fatal(e)
	}
	if e = s.store.Moderate(host.Identity, guest.Identity, "kick", false); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.store.Authenticate(secret); ok {
		t.Fatal("kicked session alive")
	}
	if _, _, e = s.store.JoinInvite(code, "New browser"); e == nil {
		t.Fatal("kicked guest bypassed with same invite")
	}
	_, code = mustInvite(t, s.store, u, r, "presenter", 1)
	p, _, _ := s.store.JoinInvite(code, "Expiring")
	if _, e = s.store.db.Exec("UPDATE sessions SET expires=unixepoch()-1 WHERE identity=?", p.Identity); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.store.Get(p.Identity); ok {
		t.Fatal("expired session active")
	}
	if e = s.store.CloseRoom(u.ID, r.ID); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.store.Get(host.Identity); ok {
		t.Fatal("closed room active")
	}
}
func TestSchemaCompatibilityAndLegacyImport(t *testing.T) {
	s := setup(t)
	legacy := map[string]Session{}
	id := strings.Repeat("a", 64)
	secret := strings.Repeat("b", 64)
	legacy[id] = Session{Identity: id, Room: "meeting", Name: "Legacy", CanPublish: true, Expires: time.Now().Add(time.Hour), SecretHash: hash(secret)}
	b, _ := json.Marshal(legacy)
	path := filepath.Join(t.TempDir(), "legacy.json")
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.store.ImportLegacy(path); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.store.Authenticate(secret); !ok {
		t.Fatal("legacy identity lost")
	}
	if e := s.store.ImportLegacy(path); e == nil {
		t.Fatal("import repeated")
	}
	original, _ := os.ReadFile(path)
	if string(original) != string(b) {
		t.Fatal("source modified")
	}
	if _, e := s.store.db.Exec("PRAGMA user_version=999"); e != nil {
		t.Fatal(e)
	}
	if s.store.Healthy() {
		t.Fatal("newer schema healthy")
	}
	if db, e := OpenStore(s.store.path); e == nil {
		db.Close()
		t.Fatal("newer schema accepted")
	}
	if db, e := OpenReadOnlyStore(s.store.path); e == nil {
		db.Close()
		t.Fatal("signal accepted newer schema")
	}
}
func TestWebhookVerificationIdempotency(t *testing.T) {
	s := setup(t)
	h := s.Handler(http.NotFoundHandler())
	body := `{"id":"evt-1","event":"participant_joined","createdAt":"123"}`
	send := func(body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/livekit/webhook", strings.NewReader(body))
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	sign := func(body string) string {
		digest := sha256.Sum256([]byte(body))
		token, e := auth.NewAccessToken(s.cfg.APIKey, s.cfg.APISecret).SetSha256(base64.StdEncoding.EncodeToString(digest[:])).ToJWT()
		if e != nil {
			t.Fatal(e)
		}
		return token
	}
	token := sign(body)
	if w := send(body, ""); w.Code != 401 {
		t.Fatal("unsigned event accepted")
	}
	if w := send(body+" ", token); w.Code != 401 {
		t.Fatal("tampered event accepted")
	}
	for i := 0; i < 3; i++ {
		if w := send(body, token); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	var count int
	s.store.db.QueryRow("SELECT count(*) FROM webhook_events").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate effects")
	}
	changed := strings.Replace(body, "participant_joined", "participant_left", 1)
	if w := send(changed, sign(changed)); w.Code != 409 {
		t.Fatal("conflicting event accepted")
	}
}

type reconcileRooms struct {
	members          []*livekit.ParticipantInfo
	removed, updated int
}

func (f *reconcileRooms) ListParticipants(context.Context, *livekit.ListParticipantsRequest) (*livekit.ListParticipantsResponse, error) {
	return &livekit.ListParticipantsResponse{Participants: f.members}, nil
}
func (f *reconcileRooms) UpdateParticipant(_ context.Context, r *livekit.UpdateParticipantRequest) (*livekit.ParticipantInfo, error) {
	f.updated++
	return &livekit.ParticipantInfo{Permission: r.Permission}, nil
}
func (f *reconcileRooms) RemoveParticipant(context.Context, *livekit.RoomParticipantIdentity) (*livekit.RemoveParticipantResponse, error) {
	f.removed++
	return &livekit.RemoveParticipantResponse{}, nil
}
func TestRestartReconcilesAuthoritativeState(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "webinar")
	_, code := mustInvite(t, s.store, u, r, "attendee", 2)
	p, _, _ := s.store.JoinInvite(code, "Viewer")
	reopened, e := OpenStore(s.store.path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	app := New(s.cfg, reopened)
	fake := &reconcileRooms{members: []*livekit.ParticipantInfo{{Identity: p.Identity, Permission: permission(true)}, {Identity: "unknown", Permission: permission(true)}}}
	app.rooms = fake
	app.reconcileOnce(context.Background())
	if fake.updated != 1 || fake.removed < 1 {
		t.Fatal("restart failed to repair permissions/remove stale identity")
	}
}
