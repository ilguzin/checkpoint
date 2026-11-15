package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func localTestUser(t *testing.T, s *Store, name string) User {
	t.Helper()
	u, e := s.CreateLocalUser(name, "local user password", false)
	if e != nil {
		t.Fatal(e)
	}
	return u
}
func TestRoomAdminInvitationAndPermissions(t *testing.T) {
	server := setup(t)
	store := server.store
	owner := admin(t, store)
	member := localTestUser(t, store, "room-admin")
	foreign := localTestUser(t, store, "unrelated")
	room := mustRoom(t, store, owner, "webinar")
	inv, code, err := store.CreateRoomAdminInvite(owner.ID, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Room != room.ID || len(code) != 64 {
		t.Fatal("invalid admin invitation")
	}
	if _, err = store.AcceptRoomAdminInvite(owner.ID, code); !errors.Is(err, ErrAccess) {
		t.Fatal("owner consumed own admin invitation")
	}
	added, err := store.AcceptRoomAdminInvite(member.ID, code)
	if err != nil || added.AccessRole != "admin" {
		t.Fatalf("admin acceptance: %+v %v", added, err)
	}
	if _, err = store.AcceptRoomAdminInvite(foreign.ID, code); !errors.Is(err, ErrAccess) {
		t.Fatal("single-use invitation accepted twice")
	}
	rooms, err := store.Rooms(member.ID)
	if err != nil || len(rooms) != 1 || rooms[0].ID != room.ID || rooms[0].AccessRole != "admin" {
		t.Fatalf("assigned room missing: %+v %v", rooms, err)
	}
	admins, err := store.RoomAdmins(owner.ID, room.ID)
	if err != nil || len(admins) != 1 || admins[0].ID != member.ID {
		t.Fatalf("admin list: %+v %v", admins, err)
	}
	host, _, err := store.JoinRoom(owner.ID, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.MeetingPolicy(host.Identity, false, true); err != nil {
		t.Fatal(err)
	}
	adminSession, adminSecret, err := store.JoinRoom(member.ID, room.ID)
	if err != nil || adminSession.Role != "moderator" || !adminSession.Moderator || !adminSession.CanPublish || !adminSession.Admitted || adminSession.UserID != member.ID {
		t.Fatalf("room admin rights: %+v %v", adminSession, err)
	}
	guest, _, err := store.JoinRoomID(room.ID, "Waiting guest")
	if err != nil || guest.Admitted {
		t.Fatal("guest did not wait")
	}
	if err = store.Moderate(adminSession.Identity, guest.Identity, "admit", false); err != nil {
		t.Fatal(err)
	}
	if err = store.Moderate(adminSession.Identity, guest.Identity, "publish", true); err != nil {
		t.Fatal(err)
	}
	if err = store.Moderate(adminSession.Identity, guest.Identity, "publish", false); err != nil {
		t.Fatal(err)
	}
	if err = store.SendMessage(guest.Identity, "question", "Can I speak?"); err != nil {
		t.Fatal(err)
	}
	state, err := store.MeetingState(adminSession.Identity)
	if err != nil || len(state.Messages) != 1 {
		t.Fatalf("meeting state: %v", err)
	}
	if err = store.Answer(adminSession.Identity, state.Messages[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Screen(adminSession.Identity, true); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateInvite(member.ID, room.ID, "attendee", 24, 1)
	if err != nil {
		t.Fatal(err)
	}
	destructive := []struct {
		name   string
		action func() error
	}{
		{"close", func() error { return store.CloseRoom(member.ID, room.ID) }},
		{"end", func() error { return store.EndMeeting(adminSession.Identity) }},
		{"policy", func() error { return store.MeetingPolicy(adminSession.Identity, true, false) }},
		{"kick", func() error { return store.Moderate(adminSession.Identity, guest.Identity, "kick", false) }},
		{"revoke invite", func() error { return store.RevokeInvite(member.ID, room.ID, "anything") }},
		{"create admin", func() error { _, _, e := store.CreateRoomAdminInvite(member.ID, room.ID); return e }},
		{"remove admin", func() error { return store.RemoveRoomAdmin(member.ID, room.ID, member.ID) }},
		{"list admins", func() error { _, e := store.RoomAdmins(member.ID, room.ID); return e }},
		{"create moderator", func() error { _, _, e := store.CreateInvite(member.ID, room.ID, "moderator", 24, 1); return e }},
	}
	for _, check := range destructive {
		if e := check.action(); !errors.Is(e, ErrAccess) {
			t.Fatalf("admin allowed %s: %v", check.name, e)
		}
	}
	ownRoom := mustRoom(t, store, member, "meeting")
	_, ownSecret, err := store.JoinRoom(member.ID, ownRoom.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RemoveRoomAdmin(owner.ID, room.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(adminSecret); ok {
		t.Fatal("removed admin session remains")
	}
	if _, ok := store.Authenticate(ownSecret); !ok {
		t.Fatal("removing membership revoked unrelated own room")
	}
	if ordinary, _, e := store.JoinRoom(member.ID, room.ID); e != nil || ordinary.Moderator || ordinary.Role != "attendee" {
		t.Fatal("removed admin must keep only ordinary membership", e)
	}
	rooms, err = store.Rooms(member.ID)
	if err != nil || len(rooms) != 2 {
		t.Fatalf("ordinary membership lost: %+v %v", rooms, err)
	}
	for _, listed := range rooms {
		if listed.ID == room.ID && listed.AccessRole != "shared" {
			t.Fatal("revoked admin still elevated")
		}
	}
}

func TestRoomAdminInvitationHTTPAndRevocation(t *testing.T) {
	server := setup(t)
	store := server.store
	owner := admin(t, store)
	member := localTestUser(t, store, "http-admin")
	room := mustRoom(t, store, owner, "meeting")
	handler := server.Handler(http.NotFoundHandler())
	_, ownerLogin, err := store.Login(owner.Username, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	_, memberLogin, err := store.Login(member.Username, "local user password")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/rooms/" + room.ID
	response := accountRequest(handler, "POST", base+"/admin-invitations", ownerLogin, "{}", server.cfg.PublicOrigin)
	if response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	var created struct {
		Invitation RoomAdminInvitation
		URL        string
	}
	if err = json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	code := created.URL[len(server.cfg.PublicOrigin+"/#adminInvite="):]
	body := fmt.Sprintf(`{"invite":%q}`, code)
	if w := accountRequest(handler, "POST", "/api/room-admin-invitations/accept", "", body, server.cfg.PublicOrigin); w.Code != 401 {
		t.Fatal("unauthenticated acceptance allowed")
	}
	if w := accountRequest(handler, "POST", "/api/room-admin-invitations/accept", memberLogin, body, ""); w.Code != 403 {
		t.Fatal("cross-origin acceptance allowed")
	}
	if w := accountRequest(handler, "POST", "/api/room-admin-invitations/accept", memberLogin, body, server.cfg.PublicOrigin); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := accountRequest(handler, "GET", base+"/admins", ownerLogin, "", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, route := range []struct{ method, path, body string }{{"GET", base + "/admins", ""}, {"POST", base + "/admin-invitations", "{}"}, {"POST", base + "/admins/" + member.ID + "/remove", "{}"}, {"POST", base + "/close", "{}"}, {"POST", base + "/admin-invitations/" + created.Invitation.ID + "/revoke", "{}"}} {
		w := accountRequest(handler, route.method, route.path, memberLogin, route.body, server.cfg.PublicOrigin)
		if w.Code != 403 {
			t.Fatalf("room admin forbidden endpoint %s: %d %s", route.path, w.Code, w.Body.String())
		}
	}
	_, secret, err := store.JoinRoom(member.ID, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w := accountRequest(handler, "POST", base+"/admins/"+member.ID+"/remove", ownerLogin, "{}", server.cfg.PublicOrigin); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, ok := store.Authenticate(secret); ok {
		t.Fatal("HTTP removal left session active")
	}
	for _, mode := range []string{"expired", "revoked", "closed", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			r := mustRoom(t, store, owner, "meeting")
			invite, token, e := store.CreateRoomAdminInvite(owner.ID, r.ID)
			if e != nil {
				t.Fatal(e)
			}
			target := member
			switch mode {
			case "expired":
				_, e = store.db.Exec("UPDATE room_admin_invites SET expires=unixepoch()-1 WHERE id=?", invite.ID)
			case "revoked":
				e = store.RevokeRoomAdminInvite(owner.ID, r.ID, invite.ID)
			case "closed":
				e = store.CloseRoom(owner.ID, r.ID)
			case "disabled":
				target = localTestUser(t, store, "disabled-admin")
				e = store.DisableLocalUser(target.Username)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = store.AcceptRoomAdminInvite(target.ID, token); !errors.Is(e, ErrAccess) {
				t.Fatalf("%s invitation accepted: %v", mode, e)
			}
		})
	}
}

func TestRoomAdminInviteConcurrentConsumption(t *testing.T) {
	server := setup(t)
	store := server.store
	owner := admin(t, store)
	a := localTestUser(t, store, "accept-a")
	b := localTestUser(t, store, "accept-b")
	room := mustRoom(t, store, owner, "meeting")
	_, code, err := store.CreateRoomAdminInvite(owner.ID, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var successes atomic.Int32
	var group sync.WaitGroup
	for _, attempt := range []struct {
		db   *Store
		user User
	}{{store, a}, {second, b}} {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, e := attempt.db.AcceptRoomAdminInvite(attempt.user.ID, code); e == nil {
				successes.Add(1)
			} else if !errors.Is(e, ErrAccess) {
				t.Error(e)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful accepts: %d", successes.Load())
	}
	admins, err := store.RoomAdmins(owner.ID, room.ID)
	if err != nil || len(admins) != 1 {
		t.Fatal("concurrent grant count")
	}
}

func TestRoomAdminMigrationPreservesVersionTwoData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	user := localTestUser(t, store, "migration-user")
	room := mustRoom(t, store, user, "meeting")
	_, secret, err := store.JoinRoomID(room.ID, "Existing guest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("ALTER TABLE users DROP COLUMN ui_locale; DROP TABLE media_workers; DROP TABLE room_deletions; DROP TABLE recording_audit; DROP TABLE minutes_revisions; DROP TABLE recording_transcripts; DROP TABLE processing_jobs; DROP TABLE recording_runs; DROP TABLE meeting_occurrences; DROP TABLE room_memberships; ALTER TABLE rooms DROP COLUMN deletion_state; DROP TABLE room_admin_invites; DROP TABLE room_admins; PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if readonly, e := OpenReadOnlyStore(path); e == nil {
		readonly.Close()
		t.Fatal("read-only signal accepted unmigrated schema")
	}
	migrated, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if !migrated.Healthy() {
		t.Fatal("migration unhealthy")
	}
	if _, ok := migrated.Authenticate(secret); !ok {
		t.Fatal("migration lost guest session")
	}
	rooms, err := migrated.Rooms(user.ID)
	if err != nil || len(rooms) != 1 || rooms[0].ID != room.ID {
		t.Fatalf("migration lost owner rooms: %+v %v", rooms, err)
	}
	readonly, err := OpenReadOnlyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if _, ok := readonly.Authenticate(secret); !ok {
		t.Fatal("read-only signal cannot use migrated session")
	}
}
