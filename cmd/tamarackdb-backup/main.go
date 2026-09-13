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
	"log"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON configuration file")
	flag.Parse()

	if err := run(context.Background(), *configPath); err != nil {
		log.Fatalf("tamarackdb-backup: %v", err)
	}
}
