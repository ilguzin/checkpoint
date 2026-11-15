package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRoomIDFormatAndNormalization(t *testing.T) {
	seen := map[string]bool{}
	for range 128 {
		id := randomRoomID()
		if len(id) != 19 || id[4] != '-' || id[9] != '-' || id[14] != '-' {
			t.Fatalf("invalid room ID format: %q", id)
		}
		for _, char := range strings.ReplaceAll(id, "-", "") {
			if !strings.ContainsRune(roomAlphabet, char) {
				t.Fatalf("unexpected room ID character: %q", char)
			}
		}
		if seen[id] {
			t.Fatal("duplicate generated room ID")
		}
		seen[id] = true
		for _, value := range []string{id, strings.ToLower(id), strings.ReplaceAll(id, "-", ""), " \t" + strings.ReplaceAll(id, "-", " ") + "\n"} {
			if normalizedRoomID(value) != id {
				t.Fatalf("normalization failed: %q", value)
			}
		}
	}
	if normalizedRoomID("olAB-CDEF-GHJK-MNPQ") != "01AB-CDEF-GHJK-MNPQ" {
		t.Fatal("ambiguous digits not normalized")
	}
	for _, value := range []string{"", "ABC", "ABCD-EFGH-JKMN-PQRS-TUVW", "ABCD-EFGH-JKMN-PQRU", "ABCD/EFGH/JKMN/PQRS"} {
		if normalizedRoomID(value) != "" {
			t.Fatalf("invalid ID normalized: %q", value)
		}
	}
}

func TestRoomIDGuestPrivilegesAndLegacyAccess(t *testing.T) {
	server := setup(t)
	store := server.store
	owner := admin(t, store)
	for _, mode := range []string{"meeting", "webinar"} {
		room := mustRoom(t, store, owner, mode)
		guest, secret, err := store.JoinRoomID(strings.ToLower(strings.ReplaceAll(room.ID, "-", " ")), "  Guest  ")
		if err != nil {
			t.Fatal(err)
		}
		if guest.Room != room.ID || guest.Name != "Guest" || guest.Moderator || guest.Role != "attendee" || guest.UserID != "" || guest.InviteID != "" || guest.CanPublish != (mode == "meeting") {
			t.Fatalf("wrong guest rights: %+v", guest)
		}
		if _, ok := store.Authenticate(secret); !ok {
			t.Fatal("room-ID guest cannot reconnect")
		}
		if _, _, err = store.JoinAccess(room.ID, "Another"); err != nil {
			t.Fatal(err)
		}
		_, code := mustInvite(t, store, owner, room, "presenter", 2)
		invited, _, err := store.JoinAccess(code, "Invited")
		if err != nil || !invited.CanPublish || invited.InviteID == "" {
			t.Fatalf("legacy invite failed: %+v %v", invited, err)
		}
	}
	legacy, _, err := store.JoinRoomID("meeting", "Legacy guest")
	if err != nil || legacy.Room != "meeting" {
		t.Fatalf("legacy room unavailable: %v", err)
	}
	if _, _, err = store.JoinAccess(strings.Repeat("m", 32), "Legacy invite"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.JoinRoomID("no-such-room", "Unknown"); !errors.Is(err, ErrAccess) {
		t.Fatalf("unknown ID: %v", err)
	}
	if _, err = store.db.Exec("INSERT INTO rooms(id,name,mode) VALUES('probe-secret','Test probe','meeting')"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.JoinRoomID("probe-secret", "Guess"); !errors.Is(err, ErrAccess) {
		t.Fatal("probe room accepted")
	}
}

func TestRoomIDRespectsMeetingPolicies(t *testing.T) {
	server := setup(t)
	store := server.store
	owner := admin(t, store)
	room := mustRoom(t, store, owner, "meeting")
	host, _, err := store.JoinRoom(owner.ID, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.MeetingPolicy(host.Identity, false, true); err != nil {
		t.Fatal(err)
	}
	guest, secret, err := store.JoinRoomID(room.ID, "Waiting guest")
	if err != nil || guest.Admitted {
		t.Fatalf("waiting room bypassed: %+v %v", guest, err)
	}
	if err = store.Moderate(host.Identity, guest.Identity, "admit", false); err != nil {
		t.Fatal(err)
	}
	if current, ok := store.Authenticate(secret); !ok || !current.Admitted {
		t.Fatal("admission did not apply")
	}
	if err = store.MeetingPolicy(host.Identity, true, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.JoinRoomID(room.ID, "Late guest"); !errors.Is(err, ErrAccess) {
		t.Fatal("locked room accepted guest")
	}
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("lock disconnected existing guest")
	}
	if err = store.MeetingPolicy(host.Identity, false, false); err != nil {
		t.Fatal(err)
	}
	if err = store.SetMaintenance(true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.JoinRoomID(room.ID, "Maintenance guest"); !errors.Is(err, ErrAccess) {
		t.Fatal("maintenance bypassed")
	}
	if err = store.SetMaintenance(false); err != nil {
		t.Fatal(err)
	}
	if err = store.Moderate(host.Identity, guest.Identity, "kick", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(secret); ok {
		t.Fatal("kicked guest session survived")
	}
	// A shared room ID is a bearer capability; an anonymous guest can rejoin until the host locks the room.
	if _, _, err = store.JoinRoomID(room.ID, "New browser"); err != nil {
		t.Fatal(err)
	}
	if err = store.CloseRoom(owner.ID, room.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.JoinRoomID(room.ID, "Closed guest"); !errors.Is(err, ErrAccess) {
		t.Fatal("closed room accepted guest")
	}
}

func TestRoomIDSessionLimit(t *testing.T) {
	server := setup(t)
	store := server.store
	owner := admin(t, store)
	room := mustRoom(t, store, owner, "meeting")
	_, err := store.db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<1000)
 INSERT INTO sessions(identity,name,room_id,role,can_publish,expires,secret_hash)
 SELECT 'limit-'||n,'Existing',?,'attendee',1,unixepoch()+3600,'limit-secret-'||n FROM seq`, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.JoinRoomID(room.ID, "Over limit"); err == nil {
		t.Fatal("session limit bypassed")
	}
}

func TestRoomOwnershipIncludesLegacyAdministrators(t *testing.T) {
	server := setup(t)
	store := server.store
	administrator := admin(t, store)
	user, err := store.CreateLocalUser("another", "another long password", false)
	if err != nil {
		t.Fatal(err)
	}
	own := mustRoom(t, store, administrator, "meeting")
	foreign := mustRoom(t, store, user, "webinar")
	for _, item := range []struct {
		user User
		room Room
	}{{administrator, own}, {user, foreign}} {
		rooms, err := store.Rooms(item.user.ID)
		if err != nil || len(rooms) != 1 || rooms[0].ID != item.room.ID {
			t.Fatalf("room ownership leak: %+v %v", rooms, err)
		}
	}
	if _, _, err = store.JoinRoom(administrator.ID, foreign.ID); !errors.Is(err, ErrAccess) {
		t.Fatal("legacy admin joined foreign room as owner")
	}
	if _, _, err = store.CreateInvite(administrator.ID, foreign.ID, "moderator", 24, 1); !errors.Is(err, ErrAccess) {
		t.Fatal("legacy admin manages foreign invitations")
	}
	if err = store.CloseRoom(administrator.ID, foreign.ID); !errors.Is(err, ErrAccess) {
		t.Fatal("legacy admin closed foreign room")
	}
	defaultRoom, err := store.CreateRoom(user.ID, "Default meeting", "")
	if err != nil || defaultRoom.Mode != "meeting" {
		t.Fatalf("default room mode: %+v %v", defaultRoom, err)
	}
}

func TestRoomAccessHTTPAndNoWebAdministration(t *testing.T) {
	server := setup(t)
	store := server.store
	owner := admin(t, store)
	room := mustRoom(t, store, owner, "webinar")
	h := server.Handler(http.NotFoundHandler())
	for _, field := range []string{"roomId", "access"} {
		body := fmt.Sprintf(`{"%s":%q,"name":"Guest"}`, field, strings.ToLower(room.ID))
		response := request(h, "POST", "/api/join", "", body)
		if response.Code != 200 {
			t.Fatalf("%s: %s", field, response.Body.String())
		}
		var session struct {
			Role                  string
			Moderator, CanPublish bool
			Room                  string
		}
		if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		if session.Room != room.ID || session.Moderator || session.CanPublish || session.Role != "attendee" {
			t.Fatalf("HTTP guest elevated: %+v", session)
		}
	}
	for _, body := range []string{
		fmt.Sprintf(`{"roomId":%q,"invite":"bad","name":"Guest"}`, room.ID),
		fmt.Sprintf(`{"access":%q,"roomId":%q,"name":"Guest"}`, room.ID, room.ID),
		fmt.Sprintf(`{"roomId":%q,"name":"Guest","role":"owner"}`, room.ID),
		`{"name":"Guest"}`, `{"roomId":"missing","name":"Guest"}`, fmt.Sprintf(`{"roomId":%q,"name":" "}`, room.ID),
	} {
		if response := request(h, "POST", "/api/join", "", body); response.Code < 400 {
			t.Fatalf("invalid request accepted: %s", body)
		}
	}
	_, login, err := store.Login(owner.Username, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path, body string }{{"POST", "/api/users", `{"username":"remote","password":"long password here","admin":true}`}, {"GET", "/api/users", ""}, {"POST", "/api/users/" + owner.ID + "/disable", "{}"}, {"GET", "/api/admin/metrics", ""}} {
		response := accountRequest(h, route.method, route.path, login, route.body, server.cfg.PublicOrigin)
		if response.Code != 404 && response.Code != 405 {
			t.Fatalf("web administration route remains: %s %s -> %d", route.method, route.path, response.Code)
		}
	}
	users, err := store.ListLocalUsers()
	if err != nil || len(users) != 1 {
		t.Fatalf("web route created account: %+v %v", users, err)
	}
}

func TestLocalAccountManagementRevokesAccess(t *testing.T) {
	server := setup(t)
	store := server.store
	administrator := admin(t, store)
	user, err := store.CreateLocalUser("local-user", "another long password", false)
	if err != nil {
		t.Fatal(err)
	}
	if user.Admin {
		t.Fatal("local ordinary user unexpectedly admin")
	}
	room := mustRoom(t, store, administrator, "meeting")
	_, login, err := store.Login(administrator.Username, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := store.JoinRoom(administrator.ID, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DisableLocalUser(" ADMIN "); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Account(login); ok {
		t.Fatal("disabled account login remains")
	}
	if _, ok := store.Authenticate(session); ok {
		t.Fatal("disabled account session remains")
	}
	if _, err = store.CreateRoom(administrator.ID, "Disabled room", "meeting"); !errors.Is(err, ErrAccess) {
		t.Fatal("disabled user created room")
	}
	rooms, err := store.Rooms(administrator.ID)
	if err != nil || len(rooms) != 0 {
		t.Fatal("disabled user lists rooms")
	}
	localRooms, err := store.ListLocalRooms()
	if err != nil || len(localRooms) < 1 {
		t.Fatal("local room listing failed")
	}
	if err = store.CloseLocalRoom(strings.ToLower(room.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.JoinRoomID(room.ID, "After CLI close"); !errors.Is(err, ErrAccess) {
		t.Fatal("CLI close did not apply")
	}
}
