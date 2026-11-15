package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/ilguzin/checkpoint/internal/core"
	"github.com/ilguzin/checkpoint/internal/release"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Set only by a release build. An unconfigured development binary fails closed.
var releasePublicKey string

func updateCommand(args []string) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	check := flags.Bool("check", false, "verify latest release without changing services")
	recoverUpdate := flags.Bool("recover", false, "recover the interrupted local update")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 || *check && *recoverUpdate {
		return errors.New("usage: checkpoint update [--check|--recover]")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("only linux/amd64 supported")
	}
	if !*check && os.Geteuid() != 0 {
		return errors.New("applying/recovering an update requires root on the target VPS")
	}
	unlock, e := release.Lock("/run/checkpoint-update.lock")
	if e != nil {
		return e
	}
	defer unlock()
	c := release.InstallConfig{Releases: "/opt/checkpoint/releases", Current: "/opt/checkpoint/current", Database: "/var/lib/checkpoint/checkpoint.db", ConfigDir: "/etc/checkpoint", Backups: "/var/backups/checkpoint", State: "/etc/checkpoint/update-state.json", Journal: "/etc/checkpoint/update-journal.json", SetupState: "/etc/checkpoint/setup-state.json"}
	c.ValidateCandidate = func(path string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "runuser", "-u", "nobody", "--", path, "trust-key").Output()
		if err != nil || strings.TrimSpace(string(output)) != releasePublicKey {
			return errors.New("candidate release key differs; key rotation requires an explicit procedure")
		}
		return nil
	}
	c.Service = func(action string, names ...string) error {
		names = updateServiceNames(names, func(name string) bool {
			return exec.Command("systemctl", "is-enabled", "--quiet", name).Run() == nil
		})
		argv := append([]string{action}, names...)
		cmd := exec.Command("systemctl", argv...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	// Keep using the already running, trusted binary if current points at a
	// broken candidate during recovery. An operator can invoke the old release.
	helper, e := os.Executable()
	if e != nil {
		return e
	}
	c.Maintenance = func(on bool) error {
		value := "off"
		if on {
			value = "on"
		}
		cmd := exec.Command("runuser", "-u", "checkpoint", "--", "env", "CHECKPOINT_DATABASE="+c.Database, helper, "maintenance", value)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	c.Idle = func() error {
		store, err := core.OpenReadOnlyStore(c.Database)
		if err != nil {
			return err
		}
		busy, err := store.MediaWorkPending()
		store.Close()
		if err != nil {
			return err
		}
		if busy {
			return errors.New("recording or minutes jobs are active; finish or cancel them before updating")
		}

		// Read only the two needed credentials; never source shell configuration.
		b, e := os.ReadFile("/etc/checkpoint/core.env")
		if e != nil {
			return e
		}
		values := parseEnvironment(string(b))
		client := lksdk.NewRoomServiceClient("http://127.0.0.1:7880", values["LIVEKIT_API_KEY"], values["LIVEKIT_API_SECRET"])
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rooms, e := client.ListRooms(ctx, &livekit.ListRoomsRequest{})
		if e != nil {
			return e
		}
		for _, room := range rooms.Rooms {
			if room.NumParticipants > 0 {
				return errors.New("active calls exist; update deferred, try again after they end")
			}
		}
		return nil
	}
	c.Health = func() error {
		client := http.Client{Timeout: 2 * time.Second}
		for attempt := 0; attempt < 15; attempt++ {
			ok := true
			for _, url := range []string{"http://127.0.0.1:18180/readyz", "http://127.0.0.1:18181/healthz", "http://127.0.0.1:18182/healthz"} {
				res, e := client.Get(url)
				if e != nil {
					ok = false
					continue
				}
				res.Body.Close()
				if res.StatusCode != 200 {
					ok = false
				}
			}
			if ok {
				return nil
			}
			time.Sleep(time.Second)
		}
		return errors.New("updated services did not become ready")
	}
	if *recoverUpdate {
		return release.Recover(c)
	}
	if releasePublicKey == "" {
		return errors.New("this build has no pinned release key; install a signed owner build")
	}
	current, e := release.ReadState(c.State)
	if e != nil {
		return e
	}
	m, raw, e := release.Latest(releasePublicKey, current)
	if e != nil {
		return e
	}
	if *check || m.Sequence == current.Sequence {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"available": m.Sequence > current.Sequence, "version": m.Version, "sequence": m.Sequence, "verified": true})
	}
	var asset release.Asset
	for _, a := range m.Assets {
		if a.Kind == "checkpoint" {
			asset = a
		}
	}
	bundle, e := release.FetchAsset(nil, "https://github.com/"+release.Repository+"/releases/download/"+m.Version, asset)
	if e != nil {
		return e
	}
	if e = release.Apply(c, m, raw, bundle); e != nil {
		return e
	}
	fmt.Println("Verified update installed. A pre-update backup was retained.")
	return nil
}

func parseEnvironment(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

// Workers use the same binary and must follow a core switch/recovery. Keep
// deliberately disabled optional services disabled; the generator is independent.
func updateServiceNames(names []string, enabled func(string) bool) []string {
	for _, name := range names {
		if name == "checkpoint-core" {
			out := append([]string{}, names...)
			for _, worker := range []string{"checkpoint-recorder", "checkpoint-processor"} {
				if enabled(worker) {
					out = append(out, worker)
				}
			}
			return out
		}
	}
	return names
}
