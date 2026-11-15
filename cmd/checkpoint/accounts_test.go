package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ilguzin/checkpoint/internal/core"
)

func privatePassword(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalUsersCommandLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	t.Setenv("CHECKPOINT_DATABASE", path)
	const password = "test-only-local-password"
	passwordFile := privatePassword(t, password+"\n")
	var output bytes.Buffer
	add := []string{"add", "--username", "alice", "--password-file", passwordFile}
	if err := runUsersCommand(add, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), password) || !strings.Contains(output.String(), "alice") {
		t.Fatalf("unsafe or missing creation result: %s", output.String())
	}
	if err := runUsersCommand(add, &output); err == nil {
		t.Fatal("duplicate account was accepted")
	}
	store, err := core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	user, login, err := store.Login("alice", password)
	if err != nil || user.Admin {
		t.Fatalf("first local account must work without bootstrap and must be an organizer: %+v %v", user, err)
	}
	room, err := store.CreateRoom(user.ID, "Alice's room", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := store.JoinRoom(user.ID, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	output.Reset()
	if err = runUsersCommand([]string{"list"}, &output); err != nil {
		t.Fatal(err)
	}
	var users []core.User
	if err = json.Unmarshal(output.Bytes(), &users); err != nil || len(users) != 1 || users[0].Username != "alice" || users[0].Admin || users[0].Disabled {
		t.Fatalf("unexpected account list: %s, %v", output.String(), err)
	}
	for _, forbidden := range []string{password, login, session, "password_hash", "$2a$"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatal("account list exposed a credential")
		}
	}
	if err = runUsersCommand([]string{"disable", "--username", "alice"}, &output); err != nil {
		t.Fatal(err)
	}
	store, err = core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, ok := store.Account(login); ok {
		t.Fatal("disabled account retained its login")
	}
	if _, ok := store.Authenticate(session); ok {
		t.Fatal("disabled account retained its participant session")
	}
	if _, _, err := store.Login("alice", password); err == nil {
		t.Fatal("disabled account could sign in")
	}
	if err := runUsersCommand([]string{"disable", "--username", "missing"}, &output); err == nil {
		t.Fatal("missing account disable reported success")
	}
}

func TestLocalCLICanDisableLegacyAdministrator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	t.Setenv("CHECKPOINT_DATABASE", path)
	password := privatePassword(t, "legacy-bootstrap-password\r\n")
	if err := adminCommand([]string{"init", "--username", "legacy", "--password-file", password}); err != nil {
		t.Fatal(err)
	}
	if err := adminCommand([]string{"init", "--username", "another", "--password-file", password}); err == nil {
		t.Fatal("legacy bootstrap unexpectedly accepted a second account")
	}
	var output bytes.Buffer
	if err := runUsersCommand([]string{"disable", "--username", "legacy"}, &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runUsersCommand([]string{"list"}, &output); err != nil {
		t.Fatal(err)
	}
	var users []core.User
	if err := json.Unmarshal(output.Bytes(), &users); err != nil || len(users) != 1 || !users[0].Admin || !users[0].Disabled {
		t.Fatalf("legacy account was not disabled by local CLI: %s %v", output.String(), err)
	}
}

func TestLocalRoomsCommandManagesAllOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	t.Setenv("CHECKPOINT_DATABASE", path)
	store, err := core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.CreateLocalUser("alice", "test-alice-password", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.CreateLocalUser("bob", "test-bob-password", false)
	if err != nil {
		t.Fatal(err)
	}
	room, err := store.CreateRoom(alice.ID, "Alice's room", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateRoom(bob.ID, "Bob's room", "webinar"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	var output bytes.Buffer
	if err = runRoomsCommand([]string{"list"}, &output); err != nil {
		t.Fatal(err)
	}
	var rooms []core.Room
	if err = json.Unmarshal(output.Bytes(), &rooms); err != nil || len(rooms) != 2 {
		t.Fatalf("local rooms list must include both owners: %s %v", output.String(), err)
	}
	if err = runRoomsCommand([]string{"close", "--id", room.ID}, &output); err != nil {
		t.Fatal(err)
	}
	store, err = core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.JoinRoom(alice.ID, room.ID); err == nil {
		t.Fatal("closed room still accepts its owner")
	}
	if err := runRoomsCommand([]string{"close", "--id", "missing"}, &output); err == nil {
		t.Fatal("missing room close reported success")
	}
}

func TestPrivatePasswordFilePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		valid          bool
	}{
		{"plain", "test-only-password", true},
		{"newline", "test-only-password\n", true},
		{"crlf", strings.Repeat("a", 72) + "\r\n", true},
		{"short", "short", false},
		{"long", strings.Repeat("a", 73), false},
		{"truncated prefix", strings.Repeat("a", 72) + "\r\nextra", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := readPrivatePassword(privatePassword(t, tc.contents))
			if (err == nil) != tc.valid {
				t.Fatalf("accepted=%v, expected=%v: %v", err == nil, tc.valid, err)
			}
			if tc.valid && (len(value) < 12 || len(value) > 72) {
				t.Fatal("password length was not validated")
			}
		})
	}
	path := privatePassword(t, "test-only-password")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivatePassword(path); err == nil {
		t.Fatal("public password file accepted")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(t.TempDir(), "symlink")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivatePassword(link); err == nil {
		t.Fatal("symlink password file accepted")
	}
	if _, err := readPrivatePassword(t.TempDir()); err == nil {
		t.Fatal("directory password file accepted")
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivatePassword(fifo); err == nil {
		t.Fatal("FIFO password file accepted")
	}
}

func TestLocalCommandsRejectBadArgumentsAndPublicDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	t.Setenv("CHECKPOINT_DATABASE", path)
	for _, args := range [][]string{nil, {"unknown"}, {"add"}, {"add", "--username", "alice"}, {"disable"}, {"list", "extra"}} {
		if err := runUsersCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid users args: %v", args)
		}
	}
	for _, args := range [][]string{nil, {"unknown"}, {"close"}, {"list", "extra"}} {
		if err := runRoomsCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid rooms args: %v", args)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid command unexpectedly created the database")
	}
	if err := runUsersCommand([]string{"list"}, &bytes.Buffer{}); err == nil {
		t.Fatal("listing a missing database reported success")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only listing unexpectedly created the database")
	}
	store, err := core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := runUsersCommand([]string{"list"}, &bytes.Buffer{}); err == nil {
		t.Fatal("CLI accepted a public database")
	}
	for _, command := range []func([]string, *bytes.Buffer) error{
		func(args []string, b *bytes.Buffer) error { return runUsersCommand(args, b) },
		func(args []string, b *bytes.Buffer) error { return runRoomsCommand(args, b) },
	} {
		var help bytes.Buffer
		if err := command([]string{"--help"}, &help); err != nil || !strings.Contains(help.String(), "CHECKPOINT_DATABASE") {
			t.Fatalf("local command help unavailable: %s %v", help.String(), err)
		}
	}
}

func TestExplicitShortPasswordResetIsScopedToOneAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	t.Setenv("CHECKPOINT_DATABASE", path)
	store, err := core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.CreateLocalUser("alice", "original-alice-password", false); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateLocalUser("bob", "original-bob-password", false); err != nil {
		t.Fatal(err)
	}
	secret := privatePassword(t, "Cloud321")
	args := []string{"set-password", "--username", "alice", "--password-file", secret}
	var output bytes.Buffer
	if err := runUsersCommand(args, &output); err == nil {
		t.Fatal("short reset accepted without explicit override")
	}
	if err := runUsersCommand(append(args, "--allow-short-password"), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "Cloud321") {
		t.Fatal("password printed by CLI")
	}
	if _, _, err := store.Login("alice", "Cloud321"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Login("alice", "original-alice-password"); err == nil {
		t.Fatal("old password accepted")
	}
	if _, _, err := store.Login("bob", "original-bob-password"); err != nil {
		t.Fatal("other account changed", err)
	}
	if _, err := store.CreateLocalUser("newuser", "Cloud321", false); err == nil {
		t.Fatal("global creation policy weakened")
	}
}
