// Command tamarackdb runs the TamarackDB HTTPS server: it loads the JSON
// configuration file, opens the SQLite store, starts the gatekeeper, and
// serves the HTTP API until an OS shutdown signal or a fatal storage error
// is observed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/api"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/gatekeeper"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// banner is printed to stdout on startup, generated with `figlet TamarackDB`
// (standard font).
const banner = " _____                                    _    ____  ____\n" +
	"|_   _|_ _ _ __ ___   __ _ _ __ __ _  ___| | _|  _ \\| __ )\n" +
	"  | |/ _` | '_ ` _ \\ / _` | '__/ _` |/ __| |/ / | | |  _ \\\n" +
	"  | | (_| | | | | | | (_| | | | (_| | (__|   <| |_| | |_) |\n" +
	"  |_|\\__,_|_| |_| |_|\\__,_|_|  \\__,_|\\___|_|\\_\\____/|____/\n"

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON configuration file (optional; falls back to TAMARACKDB_* environment variables)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	fmt.Print(banner)
	fmt.Printf("\ntamarackdb %s\nhttps://github.com/tamarackdb\n\n", version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("tamarackdb: %v", err)
	}

	st, err := store.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		log.Fatalf("tamarackdb: %v", err)
	}
	// st.Close() is not deferred: shutdown is ordered explicitly below,
	// not left to main's return.

	gk := gatekeeper.New()

	fatalCh := make(chan error, 1)
	srv := api.New(gk, st, api.Options{
		Version:      version,
		EnableAuth:   cfg.EnableAuth,
		AuthToken:    cfg.AuthToken,
		DefaultLimit: cfg.DefaultLimit,
		MaxLimit:     cfg.MaxLimit,
		MaxEventSize: cfg.MaxEventSize,
		OnFatalStorageError: func(err error) {
			select {
			case fatalCh <- err:
			default: // one signal is enough; a shutdown is already in flight
			}
		},
	})

	httpServer := &http.Server{
		Addr:              net.JoinHostPort(cfg.BindAddress, strconv.Itoa(cfg.Port)),
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("tamarackdb: listening on %s (tls=%t, auth=%t)", httpServer.Addr, cfg.EnableTLS, cfg.EnableAuth)

	serveErrCh := make(chan error, 1)
	if cfg.EnableTLS {
		go func() { serveErrCh <- httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile) }()
	} else {
		go func() { serveErrCh <- httpServer.ListenAndServe() }()
	}

	exitCode := 0
	select {
	case <-signalCtx.Done():
		log.Print("tamarackdb: received shutdown signal")
	case err := <-fatalCh:
		log.Printf("tamarackdb: fatal storage error: %v", err)
		exitCode = 1
	case err := <-serveErrCh:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("tamarackdb: HTTP server error: %v", err)
			exitCode = 1
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("tamarackdb: graceful shutdown error: %v", err)
	}
	gk.Close()
	if err := st.Close(); err != nil {
		log.Printf("tamarackdb: store close error: %v", err)
	}
	os.Exit(exitCode)
}
