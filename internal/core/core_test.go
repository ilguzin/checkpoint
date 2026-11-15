package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

type fakeRooms struct{ updated bool }

func (f *fakeRooms) ListParticipants(context.Context, *livekit.ListParticipantsRequest) (*livekit.ListParticipantsResponse, error) {
	return &livekit.ListParticipantsResponse{}, nil
}
func (f *fakeRooms) UpdateParticipant(_ context.Context, r *livekit.UpdateParticipantRequest) (*livekit.ParticipantInfo, error) {
	f.updated = true
	return &livekit.ParticipantInfo{Permission: r.Permission}, nil
}
func (f *fakeRooms) RemoveParticipant(context.Context, *livekit.RoomParticipantIdentity) (*livekit.RemoveParticipantResponse, error) {
	return &livekit.RemoveParticipantResponse{}, nil
}
func setup(t *testing.T) *Server {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "checkpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{APIKey: "test-key", APISecret: strings.Repeat("s", 32), LiveKitURL: "http://127.0.0.1:9999", SignalURL: "wss://rtc.example", PublicOrigin: "https://cp.example", ObserverURL: "https://status.example", Invites: []Invite{{Code: strings.Repeat("h", 32), Room: "webinar", Moderator: true, CanPublish: true}, {Code: strings.Repeat("v", 32), Room: "webinar"}, {Code: strings.Repeat("m", 32), Room: "meeting", CanPublish: true}}}
	if err := store.SeedInvites(cfg.Invites); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := New(cfg, store)
	s.rooms = &fakeRooms{}
	return s
}
func request(h http.Handler, method, path, secret, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestInvitationCannotChooseRoomOrRole(t *testing.T) {
	s := setup(t)
	h := s.Handler(http.NotFoundHandler())
	for _, body := range []string{`{"invite":"invalid","name":"Tester"}`, `{"invite":"` + strings.Repeat("v", 32) + `","name":"Tester","canPublish":true}`, `{"invite":"` + strings.Repeat("v", 32) + `","name":"Tester","room":"meeting"}`} {
		if w := request(h, "POST", "/api/join", "", body); w.Code < 400 {
			t.Fatal("untrusted role accepted")
		}
	}
	w := request(h, "POST", "/api/join", "", `{"invite":"`+strings.Repeat("v", 32)+`","name":"Tester"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var out struct {
		Token, Identity string
		CanPublish      bool
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	v, err := auth.ParseAPIToken(out.Token)
	if err != nil {
		t.Fatal(err)
	}
	_, grants, err := v.Verify(s.cfg.APISecret)
	if err != nil {
		t.Fatal(err)
	}
	if out.CanPublish || grants.Video.Room != "webinar" || grants.Video.GetCanPublish() || grants.Video.RoomAdmin || grants.Video.GetCanPublishData() {
		t.Fatal("overprivileged token")
	}
}

func TestPermissionAuthorizationAndPersistence(t *testing.T) {
	s := setup(t)
	h := s.Handler(http.NotFoundHandler())
	host, hs, _ := s.store.Add("Host", "webinar", true, true)
	viewer, vs, _ := s.store.Add("Viewer", "webinar", false, false)
	other, _, _ := s.store.Add("Other", "meeting", false, true)
	for _, tc := range []struct {
		secret, id string
		code       int
	}{{vs, viewer.Identity, 403}, {hs, other.Identity, 403}, {hs, host.Identity, 403}, {"", viewer.Identity, 401}, {hs, viewer.Identity, 200}} {
		body := `{"identity":"` + tc.id + `","canPublish":true}`
		if w := request(h, "POST", "/api/permissions", tc.secret, body); w.Code != tc.code {
			t.Fatalf("got %d want %d: %s", w.Code, tc.code, w.Body)
		}
	}
	reopened, err := OpenStore(s.store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	p, ok := reopened.Authenticate(vs)
	if !ok || !p.CanPublish {
		t.Fatal("role did not survive restart")
	}
	if err := reopened.SetPublish(p.Identity, false); err != nil {
		t.Fatal(err)
	}
	p, _ = reopened.Get(p.Identity)
	if p.CanPublish {
		t.Fatal("demotion lost")
	}
}

func TestSignalGatewayRewritesOldElevatedTokens(t *testing.T) {
	s := setup(t)
	p, _, _ := s.store.Add("Viewer", "webinar", false, false)
	oldToken, err := s.token(p, true)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		v, err := auth.ParseAPIToken(r.URL.Query().Get("access_token"))
		if err != nil {
			t.Error(err)
			return
		}
		_, g, err := v.Verify(s.cfg.APISecret)
		if err != nil {
			t.Error(err)
			return
		}
		if g.Video.GetCanPublish() || g.Video.GetCanPublishData() || g.Video.Room != "webinar" || r.Header.Get("Authorization") != "" {
			t.Error("old privileges reached LiveKit")
		}
		_, _ = io.WriteString(w, "success")
	}))
	defer backend.Close()
	s.cfg.LiveKitURL = backend.URL
	h := s.SignalHandler()
	if w := request(h, "GET", "/rtc/validate?access_token="+oldToken, "", ""); w.Code != 200 || !seen {
		t.Fatalf("gateway failed: %d", w.Code)
	}
	for _, path := range []string{"/twirp/livekit.RoomService/ListRooms", "/rtc/other"} {
		if w := request(h, "GET", path, oldToken, ""); w.Code != 404 {
			t.Fatal("management path exposed")
		}
	}
	if w := request(h, "GET", "/rtc?access_token=bad", "", ""); w.Code != 401 {
		t.Fatal("bad signature accepted")
	}
	foreign := p
	foreign.Room = "meeting"
	foreign.MediaRoom = "meeting"
	wrongRoom, _ := s.token(foreign, true)
	if w := request(h, "GET", "/rtc?access_token="+wrongRoom, "", ""); w.Code != 403 {
		t.Fatal("wrong room accepted")
	}
	r := httptest.NewRequest("GET", "/rtc?access_token="+oldToken, nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
}

func TestStoreCorruptionFailsClosed(t *testing.T) {
	s := setup(t)
	if _, ok := s.store.Authenticate("bad"); ok {
		t.Fatal("invalid session")
	}
	if err := ValidateConfig(Config{}); err == nil {
		t.Fatal("missing credentials accepted")
	}
	s.store.Close()
	for _, data := range []string{"{broken", "null"} {
		if err := os.WriteFile(s.store.path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(s.store.path); err == nil {
			t.Fatal("corrupt store accepted")
		}
	}
}
