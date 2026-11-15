package core

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/livekit/protocol/livekit"
)

func TestWaitingRoomAdmissionAndMeetingGeneration(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "meeting")
	host, _, _ := s.store.JoinRoom(u.ID, r.ID)
	if e := s.store.MeetingPolicy(host.Identity, false, true); e != nil {
		t.Fatal(e)
	}
	_, code := mustInvite(t, s.store, u, r, "attendee", 10)
	guest, secret, e := s.store.JoinInvite(code, "Waiting")
	if e != nil {
		t.Fatal(e)
	}
	if guest.Admitted {
		t.Fatal("waiting guest admitted")
	}
	token, _ := s.token(guest, true)
	if w := request(s.SignalHandler(), "GET", "/rtc?access_token="+token, "", ""); w.Code != 403 {
		t.Fatal("waiting guest reached media")
	}
	if e = s.store.SendMessage(guest.Identity, "chat", "secret"); e == nil {
		t.Fatal("waiting guest sent message")
	}
	if e = s.store.Moderate(host.Identity, guest.Identity, "admit", false); e != nil {
		t.Fatal(e)
	}
	guest, ok := s.store.Authenticate(secret)
	if !ok || !guest.Admitted {
		t.Fatal("admission lost")
	}
	if e = s.store.SendMessage(guest.Identity, "chat", "<script>alert(1)</script>"); e != nil {
		t.Fatal(e)
	}
	if e = s.store.Hand(guest.Identity, true); e != nil {
		t.Fatal(e)
	}
	state, e := s.store.MeetingState(host.Identity)
	if e != nil || len(state.Messages) != 1 {
		t.Fatal("message missing", e)
	}
	if e = s.store.EndMeeting(guest.Identity); e == nil {
		t.Fatal("guest ended meeting")
	}
	oldMedia := guest.MediaRoom
	if e = s.store.EndMeeting(host.Identity); e != nil {
		t.Fatal(e)
	}
	if _, ok = s.store.Authenticate(secret); ok {
		t.Fatal("ended meeting session alive")
	}
	newHost, _, e := s.store.JoinRoom(u.ID, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if newHost.MediaRoom == oldMedia {
		t.Fatal("new meeting reused old media room")
	}
	state, e = s.store.MeetingState(newHost.Identity)
	if e != nil || len(state.Messages) != 0 {
		t.Fatal("new meeting leaked chat", e)
	}
	if w := request(s.SignalHandler(), "GET", "/rtc?access_token="+token, "", ""); w.Code != 403 {
		t.Fatal("old meeting token accepted")
	}
}
func TestScreenLeaseConcurrentAndPublishSources(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "meeting")
	a, _, _ := s.store.JoinRoom(u.ID, r.ID)
	b, _, _ := s.store.JoinRoom(u.ID, r.ID)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, id := range []string{a.Identity, b.Identity} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if s.store.Screen(id, true) == nil {
				wins.Add(1)
			}
		}(id)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("two screen leases")
	}
	a, _ = s.store.Get(a.Identity)
	b, _ = s.store.Get(b.Identity)
	if a.CanScreen == b.CanScreen {
		t.Fatal("lease not exclusive")
	}
	holder, other := a, b
	if b.CanScreen {
		holder, other = b, a
	}
	if len(sessionPermission(holder).CanPublishSources) != 4 || len(sessionPermission(other).CanPublishSources) != 2 {
		t.Fatal("screen sources not enforced")
	}
	if e := s.store.Screen(holder.Identity, false); e != nil {
		t.Fatal(e)
	}
	if e := s.store.Screen(other.Identity, true); e != nil {
		t.Fatal(e)
	}
	if _, e := s.store.db.Exec("UPDATE rooms SET screen_until=unixepoch()-1 WHERE id=?", r.ID); e != nil {
		t.Fatal(e)
	}
	other, _ = s.store.Get(other.Identity)
	if other.CanScreen {
		t.Fatal("expired lease still grants screen")
	}
}
func TestCohostAndQuestionsAfterOwnerLeaves(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "webinar")
	host, _, _ := s.store.JoinRoom(u.ID, r.ID)
	_, modCode := mustInvite(t, s.store, u, r, "moderator", 1)
	mod, _, _ := s.store.JoinInvite(modCode, "Cohost")
	_, guestCode := mustInvite(t, s.store, u, r, "attendee", 1)
	guest, _, _ := s.store.JoinInvite(guestCode, "Viewer")
	if e := s.store.Leave(host.Identity); e != nil {
		t.Fatal(e)
	}
	if e := s.store.SendMessage(guest.Identity, "question", "Can I present?"); e != nil {
		t.Fatal(e)
	}
	state, e := s.store.MeetingState(mod.Identity)
	if e != nil || len(state.Messages) != 1 {
		t.Fatal(e)
	}
	if e = s.store.Answer(guest.Identity, state.Messages[0].ID); e == nil {
		t.Fatal("viewer marked answer")
	}
	if e = s.store.Answer(mod.Identity, state.Messages[0].ID); e != nil {
		t.Fatal(e)
	}
	if e = s.store.Moderate(mod.Identity, guest.Identity, "publish", true); e != nil {
		t.Fatal(e)
	}
	if e = s.store.MeetingPolicy(mod.Identity, true, false); e == nil {
		t.Fatal("cohost changed owner access policy")
	}
	host, _, e = s.store.JoinRoom(u.ID, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.store.MeetingPolicy(host.Identity, true, false); e != nil {
		t.Fatal(e)
	}
	_, newCode := mustInvite(t, s.store, u, r, "attendee", 1)
	if _, _, e = s.store.JoinInvite(newCode, "Late"); e == nil {
		t.Fatal("locked room admitted guest")
	}
	if e = s.store.EndMeeting(mod.Identity); e == nil {
		t.Fatal("cohost ended owner room")
	}
}

type orderedRooms struct {
	rows       []*livekit.ParticipantInfo
	updates    []string
	failRevoke bool
}

func (f *orderedRooms) ListParticipants(context.Context, *livekit.ListParticipantsRequest) (*livekit.ListParticipantsResponse, error) {
	return &livekit.ListParticipantsResponse{Participants: f.rows}, nil
}
func (f *orderedRooms) UpdateParticipant(_ context.Context, r *livekit.UpdateParticipantRequest) (*livekit.ParticipantInfo, error) {
	f.updates = append(f.updates, r.Identity)
	if f.failRevoke && len(r.Permission.CanPublishSources) == 2 {
		return nil, errors.New("media unavailable")
	}
	return &livekit.ParticipantInfo{Permission: r.Permission}, nil
}
func (f *orderedRooms) RemoveParticipant(context.Context, *livekit.RoomParticipantIdentity) (*livekit.RemoveParticipantResponse, error) {
	return &livekit.RemoveParticipantResponse{}, nil
}
func TestScreenTransferRevokesBeforeGrantAndFailsClosed(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "meeting")
	old, _, _ := s.store.JoinRoom(u.ID, r.ID)
	next, secret, _ := s.store.JoinRoom(u.ID, r.ID)
	fake := &orderedRooms{rows: []*livekit.ParticipantInfo{{Identity: next.Identity}, {Identity: old.Identity}}}
	s.rooms = fake
	h := s.Handler(http.NotFoundHandler())
	if w := request(h, "POST", "/api/screen", secret, `{"claim":true}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if len(fake.updates) != 2 || fake.updates[0] != old.Identity || fake.updates[1] != next.Identity {
		t.Fatal("grant preceded revocation")
	}
	_ = s.store.Screen(next.Identity, false)
	fake.updates = nil
	fake.failRevoke = true
	if w := request(h, "POST", "/api/screen", secret, `{"claim":true}`); w.Code != 503 {
		t.Fatal("unconfirmed grant accepted")
	}
	p, _ := s.store.Get(next.Identity)
	if p.CanScreen {
		t.Fatal("failed lease retained")
	}
	if len(fake.updates) != 1 {
		t.Fatal("grant attempted after revoke failure")
	}
}

func TestRaisedHandsKeepArrivalOrder(t *testing.T) {
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "meeting")
	a, _, _ := s.store.JoinRoom(u.ID, r.ID)
	b, _, _ := s.store.JoinRoom(u.ID, r.ID)
	hand := func(id string, raised bool) int64 {
		t.Helper()
		if err := s.store.Hand(id, raised); err != nil {
			t.Fatal(err)
		}
		var stamp int64
		if err := s.store.db.QueryRow("SELECT hand_at FROM sessions WHERE identity=?", id).Scan(&stamp); err != nil {
			t.Fatal(err)
		}
		return stamp
	}
	first := hand(a.Identity, true)
	second := hand(b.Identity, true)
	if first <= 0 || second <= first {
		t.Fatal("hands must have distinct arrival positions", first, second)
	}
	if again := hand(a.Identity, true); again != first {
		t.Fatal("repeated raise changed position", again, first)
	}
	if cleared := hand(a.Identity, false); cleared != 0 {
		t.Fatal("lowering did not clear the hand", cleared)
	}
	if last := hand(a.Identity, true); last <= second {
		t.Fatal("raising again did not move to the end", last, second)
	}
}
