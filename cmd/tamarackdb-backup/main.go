// Command tamarackdb-backup performs one catch-up copy of new events from a
// remote TamarackDB instance into a local SQLite file, then exits. It is
// meant to be invoked by cron or a systemd timer, not run continuously:
// there is no HTTP server, no poll loop, and no in-process retry. If a run
// fails partway, the next scheduled run resumes from the last successfully
// imported page.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"github.com/tamarackdb/tamarackdb/internal/config"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON configuration file")
	defaultConfig := flag.Bool("default-config", false, "print a starter JSON configuration to stdout and exit")
	flag.Parse()

	if *defaultConfig {
		if err := printDefaultConfig(); err != nil {
			log.Fatalf("tamarackdb-backup: %v", err)
		}
		return
	}

	if err := run(context.Background(), *configPath); err != nil {
		log.Fatalf("tamarackdb-backup: %v", err)
	}
}

// printDefaultConfig writes a starter JSON configuration to stdout, meant to
// be piped into a file and adjusted. It spells out every key, including the
// ones config.LoadBackup would otherwise default on its own, so this is a
// complete reference of what's configurable rather than a partial file.
func printDefaultConfig() error {
	cfg := config.BackupConfig{
		SourceURL:    "https://hostname:8085",
		DatabasePath: "tamarackdb-backup.sqlite",
		PageLimit:    config.DefaultBackupPageLimit,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
