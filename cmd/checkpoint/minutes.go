package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ilguzin/checkpoint/internal/minutes"
)

func minutesPath() string { return env("CHECKPOINT_MINUTES_CONFIG", "/etc/checkpoint/minutes.yaml") }
func minutesCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: checkpoint minutes config show|validate|apply [--file PATH] | doctor | status | worker recorder|processor")
	}
	if args[0] == "worker" {
		if len(args) != 2 {
			return errors.New("worker recorder|processor required")
		}
		token, e := minutes.Credential(env("CHECKPOINT_MEDIA_TOKEN_FILE", "/etc/checkpoint/media/"+args[1]+".token"))
		if e != nil {
			return e
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return minutes.RunWorker(ctx, args[1], minutesPath(), token)
	}
	if args[0] == "config" {
		if len(args) < 2 {
			return errors.New("config show|validate|apply required")
		}
		flags := flag.NewFlagSet("minutes config", flag.ContinueOnError)
		path := flags.String("file", "", "configuration YAML file")
		if e := flags.Parse(args[2:]); e != nil {
			return e
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected arguments")
		}
		if args[1] == "show" {
			c, e := minutes.LoadConfig(minutesPath())
			if e != nil {
				return e
			}
			return json.NewEncoder(os.Stdout).Encode(c)
		}
		if *path == "" {
			return errors.New("--file is required")
		}
		body, e := os.ReadFile(*path)
		if e != nil {
			return e
		}
		c, e := minutes.ParseConfig(body)
		if e != nil {
			return e
		}
		switch args[1] {
		case "validate":
			fmt.Println("Configuration is valid")
			return nil
		case "apply":
			old, e := minutes.LoadConfig(minutesPath())
			if e != nil {
				return e
			}
			if old.Enabled && (old.Storage != c.Storage || old.Socket != c.Socket) {
				return errors.New("storage/socket changes require offline migration, not config apply")
			}
			// Settings are read at each claim/start. Existing runs retain their snapshots.
			if e = minutes.AtomicWrite(minutesPath(), body, 0600); e != nil {
				return e
			}
			fmt.Println("Configuration applied for new recordings. Active recordings keep their profile. Start/restart media services when enabling the module.")
			return nil
		default:
			return errors.New("unknown config action")
		}
	}
	if args[0] == "doctor" || args[0] == "status" {
		c, e := minutes.LoadConfig(minutesPath())
		if e != nil {
			return e
		}
		report := map[string]any{"enabled": c.Enabled, "storage": c.Storage, "socket": c.Socket, "modelsReady": c.CheckModels() == nil, "template": "basic@1", "languages": []string{"ru", "en"}}
		client := &http.Client{Timeout: 3 * time.Second}
		if response, err := client.Get(env("CHECKPOINT_MEDIA_HEALTH_URL", "http://127.0.0.1:18180/api/media-health")); err == nil {
			var runtime map[string]any
			if response.StatusCode == 200 && json.NewDecoder(response.Body).Decode(&runtime) == nil {
				report["runtime"] = runtime
			}
			response.Body.Close()
		}

		_, e = os.Stat(c.FFmpeg)
		report["ffmpegReady"] = e == nil
		conn, e := net.DialTimeout("unix", c.Socket, time.Second)
		report["coreSocketReady"] = e == nil
		if e == nil {
			conn.Close()
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	return errors.New("unknown minutes action")
}
func mediaListener(path string, handler http.Handler) (*http.Server, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0750); e != nil {
		return nil, e
	}
	// Refuse to replace a live service socket.
	if conn, e := net.DialTimeout("unix", path, time.Second); e == nil {
		conn.Close()
		return nil, errors.New("media socket already active")
	}
	if st, e := os.Lstat(path); e == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("media socket path is not a socket")
		}
		if e = os.Remove(path); e != nil {
			return nil, e
		}
	}
	listener, e := net.Listen("unix", path)
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(path, 0660); e != nil {
		listener.Close()
		return nil, e
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096}
	go server.Serve(listener)
	return server, nil
}
