package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"encoding/json"
	"github.com/ilguzin/checkpoint/internal/core"
	"github.com/ilguzin/checkpoint/internal/deploy"
	"github.com/ilguzin/checkpoint/internal/minutes"
	"github.com/ilguzin/checkpoint/internal/notices"
	"github.com/ilguzin/checkpoint/internal/observer"
	"github.com/ilguzin/checkpoint/internal/web"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// Set by the native bundle and container builds; source builds remain explicit.
var version = "M1-dev"

const commandUsage = `Usage: checkpoint COMMAND [OPTIONS]

Services:     serve, signal, observe
Recordings:   minutes config|doctor|status|worker
Accounts:     users add|list|disable|set-password  (local account administration)
Rooms:        rooms list|close       (local access to all rooms)
Operations:   status, doctor, maintenance on|off, backup, restore
Deployment:   deploy plan|apply|recover TARGET.yaml, update
Information:  version, licenses, trust-key, help
Compatibility: admin init, import-json FILE

Use checkpoint users --help or checkpoint rooms --help for administration options.
Native installations automatically use /var/lib/checkpoint/checkpoint.db.
CHECKPOINT_DATABASE overrides the database path. Run administration locally
as root or the checkpoint service user. Platform administration is not exposed on the web.
`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	log.SetFlags(0)
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "help" || command == "--help" || command == "-h" {
		fmt.Print(commandUsage)
		return
	}
	if command == "users" || command == "rooms" {
		var err error
		if command == "users" {
			err = usersCommand(os.Args[2:])
		} else {
			err = roomsCommand(os.Args[2:])
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if command == "minutes" {
		if err := minutesCommand(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if command == "deploy" {
		if len(os.Args) != 4 {
			log.Fatal("usage: checkpoint deploy plan|apply|recover TARGET.yaml")
		}
		body, err := os.ReadFile(os.Args[3])
		if err != nil {
			log.Fatal(err)
		}
		target, err := deploy.Parse(body)
		if err != nil {
			log.Fatal(err)
		}
		switch os.Args[2] {
		case "plan":
			err = json.NewEncoder(os.Stdout).Encode(target.Plan())
		case "apply":
			err = deploy.Apply(target, releasePublicKey)
		case "recover":
			err = deploy.Recover(target)
		default:
			err = errors.New("unknown deployment action")
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if command == "update" {
		if err := updateCommand(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if command == "trust-key" {
		if releasePublicKey == "" {
			log.Fatal("no release key configured")
		}
		fmt.Println(releasePublicKey)
		return
	}
	if command == "licenses" {
		_, _ = os.Stdout.Write(notices.Catalog)
		return
	}
	if command == "version" {
		fmt.Println("checkpoint " + version)
		return
	}
	if command == "backup" || command == "restore" || command == "status" || command == "doctor" || command == "maintenance" {
		if err := operationsCommand(command, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if command == "admin" {
		if err := adminCommand(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg := core.Config{MinutesConfig: minutesPath(), APIKey: os.Getenv("LIVEKIT_API_KEY"), APISecret: os.Getenv("LIVEKIT_API_SECRET"), LiveKitURL: env("LIVEKIT_URL", "http://127.0.0.1:17880"), SignalURL: os.Getenv("CHECKPOINT_SIGNAL_URL"), PublicOrigin: os.Getenv("CHECKPOINT_PUBLIC_URL"), ObserverURL: os.Getenv("CHECKPOINT_OBSERVER_URL"), Invites: []core.Invite{
		{Code: os.Getenv("CHECKPOINT_MEETING_INVITE"), Room: "m0a-meeting", CanPublish: true},
		{Code: os.Getenv("CHECKPOINT_HOST_INVITE"), Room: "m0a-webinar", Moderator: true, CanPublish: true},
		{Code: os.Getenv("CHECKPOINT_VIEWER_INVITE"), Room: "m0a-webinar"},
	}}
	filtered := cfg.Invites[:0]
	for _, inv := range cfg.Invites {
		if inv.Code != "" {
			filtered = append(filtered, inv)
		}
	}
	cfg.Invites = filtered
	var servers []*http.Server
	var listenerFailed atomic.Bool
	start := func(addr string, handler http.Handler) {
		srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		servers = append(servers, srv)
		go func() {
			log.Printf("%s listening on %s", command, addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Print(err)
				listenerFailed.Store(true)
				cancel()
			}
		}()
	}
	switch command {
	case "serve":
		if err := core.ValidateConfig(cfg); err != nil {
			log.Fatal(err)
		}
		store, err := core.OpenStore(databasePath())
		if err != nil {
			log.Fatal(err)
		}
		defer store.Close()
		if err = store.SeedInvites(cfg.Invites); err != nil {
			log.Fatal(err)
		}
		app := core.New(cfg, store)
		if err = app.PrepareMedia(); err != nil {
			log.Fatal(err)
		}
		mediaConfig, mediaError := minutes.LoadConfig(minutesPath())
		if mediaError == nil && mediaConfig.Enabled {
			recorderToken, recErr := minutes.Credential(env("CHECKPOINT_RECORDER_TOKEN_FILE", "/etc/checkpoint/media/recorder.token"))
			processorToken, procErr := minutes.Credential(env("CHECKPOINT_PROCESSOR_TOKEN_FILE", "/etc/checkpoint/media/processor.token"))
			if recErr != nil || procErr != nil {
				mediaError = errors.New("media worker credentials unavailable")
			} else {
				mediaServer, err := mediaListener(mediaConfig.Socket, app.MediaHandler(recorderToken, processorToken))
				if err != nil {
					mediaError = err
				} else {
					servers = append(servers, mediaServer)
				}
			}
		}
		if mediaError != nil {
			log.Printf("Optional recording service unavailable: %v", mediaError)
		}

		go app.MaintainMedia(ctx)
		start(env("CHECKPOINT_LISTEN", "127.0.0.1:18180"), app.Handler(web.Handler("serve")))
		go app.Reconcile(ctx)
	case "signal":
		if err := core.ValidateConfig(cfg); err != nil {
			log.Fatal(err)
		}
		store, err := core.OpenReadOnlyStore(databasePath())
		if err != nil {
			log.Fatal(err)
		}
		defer store.Close()
		start(env("CHECKPOINT_SIGNAL_LISTEN", "127.0.0.1:18181"), core.New(cfg, store).SignalHandler())
	case "import-json":
		if len(os.Args) != 3 {
			log.Fatal("usage: checkpoint import-json FILE")
		}
		store, err := core.OpenStore(databasePath())
		if err != nil {
			log.Fatal(err)
		}
		defer store.Close()
		if err = store.SeedInvites(cfg.Invites); err != nil {
			log.Fatal(err)
		}
		if err = store.ImportLegacy(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		log.Print("Legacy sessions imported; source unchanged")
		return
	case "observe":
		if cfg.APIKey == "" || len(cfg.APISecret) < 32 {
			log.Fatal("LiveKit credentials required")
		}
		obs := observer.New(env("CHECKPOINT_CORE_HEALTH_URL", "http://127.0.0.1:18180/healthz"), cfg.LiveKitURL, cfg.APIKey, cfg.APISecret)
		if path := os.Getenv("CHECKPOINT_OBSERVER_DATABASE"); path != "" {
			history, err := observer.OpenHistory(path)
			if err != nil {
				log.Fatal(err)
			}
			defer history.Close()
			obs.AttachHistory(history)
		}
		start(env("CHECKPOINT_OBSERVER_LISTEN", "127.0.0.1:18182"), obs.Handler(web.Handler("observe")))
		go obs.Run(ctx)
	default:
		log.Fatal(commandUsage)
	}
	<-ctx.Done()
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdown)
	}
	if listenerFailed.Load() {
		os.Exit(1)
	}
}
