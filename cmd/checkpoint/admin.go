package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/ilguzin/checkpoint/internal/core"
)

func adminCommand(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return errors.New("usage: checkpoint admin init --username NAME --password-file FILE")
	}
	flags := flag.NewFlagSet("admin init", flag.ContinueOnError)
	name := flags.String("username", "", "first administrator username")
	path := flags.String("password-file", "", "private regular file, 0600, containing password")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || *name == "" {
		return errors.New("username and password-file are required")
	}
	password, err := readPrivatePassword(*path)
	if err != nil {
		return err
	}
	store, err := core.OpenStore(databasePath())
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err = store.CreateUser("", *name, password, true, true); err != nil {
		return err
	}
	fmt.Println("Legacy bootstrap account created. Web access follows room ownership and room administrator grants. Use checkpoint users add for additional organizers.")
	return nil
}

// Reuse the same secret-file policy for legacy bootstrap and local account creation.
func readPrivatePassword(path string) (string, error) {
	return readPrivatePasswordMinimum(path, 12)
}

func readPrivatePasswordMinimum(path string, minimum int) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("password file must be a regular private file (0600)")
	}
	// Do not follow a replaced symlink or block on a replaced FIFO between Lstat and Open.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 || !os.SameFile(info, opened) {
		return "", errors.New("password file must remain a regular private file (0600)")
	}
	// 72 password bytes plus an optional CRLF. Read one extra byte to reject oversized files.
	data, err := io.ReadAll(io.LimitReader(f, 75))
	if err != nil {
		return "", err
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if len(data) > 74 || len(password) < minimum || len(password) > 72 {
		return "", fmt.Errorf("password must contain %d–72 bytes", minimum)
	}
	return password, nil
}
