package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ilguzin/checkpoint/internal/core"
	"github.com/ilguzin/checkpoint/internal/ops"
)

func operationsCommand(command string, args []string) error {
	switch command {
	case "maintenance":
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			return errors.New("usage: checkpoint maintenance on|off")
		}
		store, e := core.OpenStore(databasePath())
		if e != nil {
			return e
		}
		defer store.Close()
		return store.SetMaintenance(args[0] == "on")
	case "backup":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		output := flags.String("output", "", "new private .tar.gz backup file")
		configs := flags.String("config-dir", "", "optional private configuration directory to include")
		if e := flags.Parse(args); e != nil {
			return e
		}
		if *output == "" || flags.NArg() != 0 {
			return errors.New("usage: checkpoint backup --output FILE [--config-dir DIRECTORY]")
		}
		if e := ops.Backup(databasePath(), *output, *configs); e != nil {
			return e
		}
		fmt.Println("Backup created. It contains private data; protect it like the live installation.")
		return nil
	case "restore":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		input := flags.String("input", "", "backup file")
		destination := flags.String("destination", "", "new database file, must not exist")
		configs := flags.String("config-output", "", "new configuration directory, must not exist")
		ledger := flags.String("deletion-ledger", "", "current recording deletion ledger (required for media restore)")
		if e := flags.Parse(args); e != nil {
			return e
		}
		if *input == "" || *destination == "" || flags.NArg() != 0 {
			return errors.New("usage: checkpoint restore --input FILE --destination NEW_DATABASE [--config-output NEW_DIRECTORY]")
		}
		if e := ops.RestoreWithMedia(*input, *destination, *configs, *ledger); e != nil {
			return e
		}
		fmt.Println("Backup verified and restored separately. Stop services before switching to this database/configuration.")
		return nil
	case "status", "doctor":
		type check struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		}
		checks := []check{}
		client := http.Client{Timeout: 3 * time.Second}
		for _, v := range []struct{ name, key, fallback string }{{"application", "CHECKPOINT_CORE_HEALTH_URL", "http://127.0.0.1:18180/healthz"}, {"signal", "CHECKPOINT_SIGNAL_HEALTH_URL", "http://127.0.0.1:18181/healthz"}, {"observer", "CHECKPOINT_OBSERVER_HEALTH_URL", "http://127.0.0.1:18182/healthz"}} {
			res, e := client.Get(env(v.key, v.fallback))
			ok := e == nil && res.StatusCode == 200
			if e == nil {
				res.Body.Close()
			}
			checks = append(checks, check{v.name, ok})
		}
		if command == "doctor" {
			db, e := core.OpenReadOnlyStore(databasePath())
			ok := e == nil
			if ok {
				db.Close()
			}
			checks = append(checks, check{"database schema and private file", ok})
		}
		if e := json.NewEncoder(os.Stdout).Encode(map[string]any{"version": version, "checks": checks}); e != nil {
			return e
		}
		for _, c := range checks {
			if !c.OK {
				return errors.New("one or more checks failed")
			}
		}
		return nil
	}
	return errors.New("unknown operation")
}
