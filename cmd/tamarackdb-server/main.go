// Command tamarackdb runs the TamarackDB HTTPS server: it loads the JSON
// configuration file, opens the SQLite store, starts the queue manager, and
// serves the HTTP API until an OS shutdown signal or a fatal storage error
// is observed.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/tamarackdb/tamarackdb/internal/buildinfo"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// banner is printed to stdout on startup, generated with `figlet TamarackDB`
// (standard font).
const banner = " _____                                    _    ____  ____\n" +
	"|_   _|_ _ _ __ ___   __ _ _ __ __ _  ___| | _|  _ \\| __ )\n" +
	"  | |/ _` | '_ ` _ \\ / _` | '__/ _` |/ __| |/ / | | |  _ \\\n" +
	"  | | (_| | | | | | | (_| | | | (_| | (__|   <| |_| | |_) |\n" +
	"  |_|\\__,_|_| |_| |_|\\__,_|_|  \\__,_|\\___|_|\\_\\____/|____/\n"

// optimizeInterval is how often the server runs PRAGMA optimize against the
// write connection (see store.Store.Optimize).
const optimizeInterval = time.Hour

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON configuration file (optional; falls back to TAMARACKDB_* environment variables)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	defaultConfig := flag.Bool("default-config", false, "print a starter JSON configuration to stdout and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	if *defaultConfig {
		if err := printDefaultConfig(); err != nil {
			log.Fatalf("tamarackdb-server: %v", err)
		}
		return
	}

	fmt.Print(banner)
	fmt.Printf("\nTamarackDB v%s\nhttps://github.com/tamarackdb\n\n", buildinfo.Version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("tamarackdb-server: %v", err)
	}

	fmt.Printf("bindAddress: %s\n", cfg.BindAddress)
	fmt.Printf("port: %d\n", cfg.Port)
	fmt.Printf("enableTls: %t\n", cfg.EnableTLS)
	fmt.Printf("tlsCertFile: %s\n", cfg.TLSCertFile)
	fmt.Printf("tlsKeyFile: %s\n", cfg.TLSKeyFile)
	fmt.Printf("enableAuth: %t\n", cfg.EnableAuth)
	fmt.Printf("databasePath: %s\n", cfg.DatabasePath)
	fmt.Printf("devMode: %t\n", cfg.DevMode)
	fmt.Printf("defaultLimit: %d\n", cfg.DefaultLimit)
	fmt.Printf("maxLimit: %d\n", cfg.MaxLimit)
	fmt.Printf("maxEventSize: %d\n", cfg.MaxEventSize)
	fmt.Printf("maxQueuedWriters: %d\n", cfg.MaxQueuedWriters)
	fmt.Printf("readPoolSize: %d\n\n", cfg.ReadPoolSize)

	st, err := store.Open(context.Background(), cfg.DatabasePath, cfg.ReadPoolSize)
	if err != nil {
		log.Fatalf("tamarackdb-server: %v", err)
	}
	// st.Close() is not deferred: shutdown is ordered explicitly below,
	// not left to main's return.

	qm := queue.New(cfg.MaxQueuedWriters)

	fatalCh := make(chan error, 1)
	srv := api.New(qm, st, api.Options{
		Version:      buildinfo.Version,
		EnableAuth:   cfg.EnableAuth,
		AuthToken:    cfg.AuthToken,
		DefaultLimit: cfg.DefaultLimit,
		MaxLimit:     cfg.MaxLimit,
		MaxEventSize: cfg.MaxEventSize,
		DevMode:      cfg.DevMode,
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

	log.Printf("tamarackdb-server: listening on %s (tls=%t, auth=%t)", httpServer.Addr, cfg.EnableTLS, cfg.EnableAuth)

	serveErrCh := make(chan error, 1)
	if cfg.EnableTLS {
		go func() { serveErrCh <- httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile) }()
	} else {
		go func() { serveErrCh <- httpServer.ListenAndServe() }()
	}

	optimizeTicker := time.NewTicker(optimizeInterval)
	defer optimizeTicker.Stop()
	go func() {
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-optimizeTicker.C:
				if err := st.Optimize(signalCtx); err != nil {
					log.Printf("tamarackdb-server: PRAGMA optimize: %v", err)
				}
			}
		}
	}()

	exitCode := 0
	select {
	case <-signalCtx.Done():
		log.Print("tamarackdb-server: received shutdown signal")
	case err := <-fatalCh:
		log.Printf("tamarackdb-server: fatal storage error: %v", err)
		exitCode = 1
	case err := <-serveErrCh:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("tamarackdb-server: HTTP server error: %v", err)
			exitCode = 1
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("tamarackdb-server: graceful shutdown error: %v", err)
	}
	qm.Close()
	if err := st.Close(); err != nil {
		log.Printf("tamarackdb-server: store close error: %v", err)
	}
	os.Exit(exitCode)
}

// printDefaultConfig writes a starter JSON configuration to stdout, meant to
// be piped into a file and adjusted. It spells out every key, including the
// ones config.Load would otherwise default on its own, so this is a
// complete reference of what's configurable rather than a partial file.
func printDefaultConfig() error {
	cfg := config.Config{
		BindAddress:      "127.0.0.1",
		Port:             8085,
		EnableTLS:        false,
		TLSCertFile:      "/path/to/cert.pem",
		TLSKeyFile:       "/path/to/key.pem",
		EnableAuth:       false,
		AuthToken:        "changeme",
		DatabasePath:     "tamarackdb.sqlite",
		DefaultLimit:     config.DefaultLimit,
		MaxLimit:         config.DefaultMaxLimit,
		MaxEventSize:     config.DefaultEventSize,
		MaxQueuedWriters: config.DefaultMaxQueuedWriters,
		ReadPoolSize:     config.DefaultReadPoolSize,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
