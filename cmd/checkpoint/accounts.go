package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ilguzin/checkpoint/internal/core"
)

const usersUsage = `usage: checkpoint users add --username NAME --password-file FILE
       checkpoint users set-password --username NAME --password-file FILE [--allow-short-password]
       checkpoint users list
       checkpoint users disable --username NAME

Account administration is local-only. Run as root or the checkpoint service user.
Native installations select their database automatically; CHECKPOINT_DATABASE
overrides the path. New accounts are organizers.
Passwords are read from private regular files; they are never command arguments.
`

func usersCommand(args []string) error { return runUsersCommand(args, os.Stdout) }

func runUsersCommand(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New(usersUsage)
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(output, usersUsage)
		return err
	}
	flags := flag.NewFlagSet("users "+args[0], flag.ContinueOnError)
	var name, path string
	var allowShort bool
	switch args[0] {
	case "add", "set-password":
		if args[0] == "set-password" {
			flags.BoolVar(&allowShort, "allow-short-password", false, "explicitly permit 8–11 bytes for this local reset only")
		}
		flags.StringVar(&name, "username", "", "organizer username")
		flags.StringVar(&path, "password-file", "", "private regular password file (0600)")
	case "disable":
		flags.StringVar(&name, "username", "", "account to disable, including legacy administrators")
	case "list":
	default:
		return errors.New(usersUsage)
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (args[0] != "list" && name == "") || ((args[0] == "add" || args[0] == "set-password") && path == "") {
		return errors.New(usersUsage)
	}
	var password string
	if args[0] == "add" || args[0] == "set-password" {
		var err error
		minimum := 12
		if allowShort {
			minimum = 8
		}
		password, err = readPrivatePasswordMinimum(path, minimum)
		if err != nil {
			return err
		}
	}
	open := core.OpenStore
	if args[0] == "list" {
		open = core.OpenReadOnlyStore
	}
	store, err := open(databasePath())
	if err != nil {
		return err
	}
	defer store.Close()
	switch args[0] {
	case "add":
		user, err := store.CreateLocalUser(name, password, false)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Organizer %s created. Sign in through Organizer sign in.\n", user.Username)
		return err
	case "set-password":
		if err := store.ResetLocalUserPassword(name, password, allowShort); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Password reset; previous account and participant sessions revoked.")
		return err
	case "list":
		users, err := store.ListLocalUsers()
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(users)
	case "disable":
		if err := store.DisableLocalUser(name); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Account disabled; its account and participant sessions are revoked.")
		return err
	}
	return nil
}

const roomsUsage = `usage: checkpoint rooms list
       checkpoint rooms close --id ROOM_ID

Lists all rooms or closes a room locally, regardless of its owner.
Use CHECKPOINT_DATABASE to select the service database.
`

func roomsCommand(args []string) error { return runRoomsCommand(args, os.Stdout) }

func runRoomsCommand(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New(roomsUsage)
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(output, roomsUsage)
		return err
	}
	flags := flag.NewFlagSet("rooms "+args[0], flag.ContinueOnError)
	var id string
	switch args[0] {
	case "close":
		flags.StringVar(&id, "id", "", "room ID from checkpoint rooms list")
	case "list":
	default:
		return errors.New(roomsUsage)
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (args[0] == "close" && id == "") {
		return errors.New(roomsUsage)
	}
	open := core.OpenStore
	if args[0] == "list" {
		open = core.OpenReadOnlyStore
	}
	store, err := open(databasePath())
	if err != nil {
		return err
	}
	defer store.Close()
	if args[0] == "list" {
		rooms, err := store.ListLocalRooms()
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(rooms)
	}
	if err := store.CloseLocalRoom(id); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Room closed. New joins and session reconnections are no longer allowed.")
	return err
}
