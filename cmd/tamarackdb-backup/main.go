// Command tamarackdb-backup performs one catch-up copy of new events from a
// remote TamarackDB instance into a local SQLite file, then exits. It is
// meant to be invoked by cron or a systemd timer, not run continuously:
// there is no HTTP server, no poll loop, and no in-process retry. If a run
// fails partway, the next scheduled run resumes from the last successfully
// imported page.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/tamarackdb/tamarackdb/internal/buildinfo"
	"github.com/tamarackdb/tamarackdb/internal/config"
)

func main() {
	configPath := flag.String("config", "config.toml", "path to the TOML configuration file")
	defaultConfig := flag.Bool("default-config", false, "print a starter TOML [backup] configuration to stdout and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	if *defaultConfig {
		printDefaultConfig()
		return
	}

	if err := run(context.Background(), *configPath); err != nil {
		log.Fatalf("tamarackdb-backup: %v", err)
	}
}

// defaultConfigTemplate is a starter [backup] TOML configuration, meant to
// be piped into a file and adjusted. It spells out every key, including the
// ones config.LoadBackup would otherwise default on its own, so this is a
// complete reference of what's configurable rather than a partial file.
// Every key is commented out at the value config.LoadBackup would apply
// anyway (or, for sourceUrl, which has no built-in default, a placeholder):
// uncommenting a line is how it takes effect. It is a hand-written
// template, not a Marshal of config.BackupConfig, so it can carry comments;
// TOML's Marshal would drop them.
const defaultConfigTemplate = `[backup]
# sourceUrl = "https://hostname:8085"
# databasePath = "%s"
# pageLimit = %d
`

// printDefaultConfig writes defaultConfigTemplate to stdout, with its
// defaults filled in from the config package's exported constants.
func printDefaultConfig() {
	fmt.Printf(defaultConfigTemplate, config.DefaultBackupDatabasePath, config.DefaultBackupPageLimit)
}
