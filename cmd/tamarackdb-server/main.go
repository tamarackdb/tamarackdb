// Command tamarackdb-server runs the TamarackDB HTTP server: it loads the TOML
// configuration file, opens the SQLite store, starts the transaction
// manager, and serves the HTTP API until an OS shutdown signal or a fatal
// storage error is observed.
package main

import (
	"context"
	"errors"
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
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/txn"
)

// banner is printed to stdout on startup, generated with `figlet TamarackDB`
// (standard font).
const banner = " _____                                    _    ____  ____\n" +
	"|_   _|_ _ _ __ ___   __ _ _ __ __ _  ___| | _|  _ \\| __ )\n" +
	"  | |/ _` | '_ ` _ \\ / _` | '__/ _` |/ __| |/ / | | |  _ \\\n" +
	"  | | (_| | | | | | | (_| | | | (_| | (__|   <| |_| | |_) |\n" +
	"  |_|\\__,_|_| |_| |_|\\__,_|_|  \\__,_|\\___|_|\\_\\____/|____/\n"

// optimizeInterval is how often the server runs PRAGMA optimize against the
// write connection, through the FIFO (see txn.Manager.Optimize).
const optimizeInterval = time.Hour

func main() {
	configPath := flag.String("config", "config.toml", "path to the TOML configuration file (optional; falls back to TAMARACKDB_* environment variables)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	defaultConfig := flag.Bool("default-config", false, "print a starter TOML [server] configuration to stdout and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	if *defaultConfig {
		printDefaultConfig()
		return
	}

	fmt.Print(banner)
	fmt.Printf("\nTamarackDB %s\nhttps://github.com/tamarackdb\n\n", buildinfo.Version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("tamarackdb-server: %v", err)
	}

	fmt.Printf("socketPath: %s\n", cfg.SocketPath)
	fmt.Printf("socketMode: %s\n", cfg.SocketMode)
	fmt.Printf("bindAddress: %s\n", cfg.BindAddress)
	fmt.Printf("port: %d\n", cfg.Port)
	fmt.Printf("enableTls: %t\n", cfg.EnableTLS)
	fmt.Printf("tlsCertFile: %s\n", cfg.TLSCertFile)
	fmt.Printf("tlsKeyFile: %s\n", cfg.TLSKeyFile)
	fmt.Printf("enableAuth: %t\n", cfg.EnableAuth)
	fmt.Printf("dataDir: %s\n", cfg.DataDir)
	fmt.Printf("devMode: %t\n", cfg.DevMode)
	fmt.Printf("logLevel: %s\n", cfg.LogLevel)
	fmt.Printf("defaultEventsPerPage: %d\n", cfg.DefaultEventsPerPage)
	fmt.Printf("maxEventsPerPage: %d\n", cfg.MaxEventsPerPage)
	fmt.Printf("maxEventSize: %d\n", cfg.MaxEventSize)
	fmt.Printf("maxProjectionSize: %d\n", cfg.MaxProjectionSize)
	fmt.Printf("maxProjectionsPerRequest: %d\n", cfg.MaxProjectionsPerRequest)
	fmt.Printf("transactionTimeout: %d\n", cfg.TransactionTimeout)
	fmt.Printf("maxTransactionDuration: %d\n", cfg.MaxTransactionDuration)
	fmt.Printf("maxQueuedTransactions: %d\n", cfg.MaxQueuedTransactions)
	fmt.Printf("readPoolSize: %d\n", cfg.ReadPoolSize)
	fmt.Printf("maxRequestBody: %d (derived from the size limits above)\n\n", api.MaxRequestBody(api.Options{
		MaxEventSize:             cfg.MaxEventSize,
		MaxProjectionSize:        cfg.MaxProjectionSize,
		MaxProjectionsPerRequest: cfg.MaxProjectionsPerRequest,
	}))

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("tamarackdb-server: create data directory: %v", err)
	}
	st, err := store.Open(context.Background(), cfg.DatabasePath(), cfg.ReadPoolSize)
	if err != nil {
		log.Fatalf("tamarackdb-server: %v", err)
	}
	// st.Close() is not deferred: shutdown is ordered explicitly below,
	// not left to main's return.

	tm, err := txn.New(st, txn.Config{
		Timeout:   time.Duration(cfg.TransactionTimeout) * time.Second,
		Ceiling:   time.Duration(cfg.MaxTransactionDuration) * time.Second,
		MaxQueued: cfg.MaxQueuedTransactions,
		PauseFile: cfg.PauseFilePath(),
		OnExpire:  api.ExpiryLogger(cfg.LogLevel),
	})
	if err != nil {
		log.Fatalf("tamarackdb-server: %v", err)
	}
	if tm.Paused() {
		log.Printf("tamarackdb-server: starting paused (%s exists)", cfg.PauseFilePath())
	}

	fatalCh := make(chan error, 1)
	srv := api.New(tm, st, api.Options{
		Version:                  buildinfo.Version,
		EnableAuth:               cfg.EnableAuth,
		AuthToken:                cfg.AuthToken,
		DefaultEventsPerPage:     cfg.DefaultEventsPerPage,
		MaxEventsPerPage:         cfg.MaxEventsPerPage,
		MaxEventSize:             cfg.MaxEventSize,
		MaxProjectionSize:        cfg.MaxProjectionSize,
		MaxProjectionsPerRequest: cfg.MaxProjectionsPerRequest,
		DevMode:                  cfg.DevMode,
		LogLevel:                 cfg.LogLevel,
		OnFatalStorageError: func(err error) {
			select {
			case fatalCh <- err:
			default: // one signal is enough; a shutdown is already in flight
			}
		},
	})

	httpServer := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		// A keep-alive connection with no request in flight is closed
		// after this long, so idle clients can't pile up open connections.
		IdleTimeout: 2 * time.Minute,
	}
	// Shutdown waits for every request in flight, and a request waiting in
	// the FIFO only ends once it gets its turn. Closing the transaction
	// manager as soon as Shutdown starts turns those requests away right
	// away, and gives out no new ticket to a client that can no longer
	// reach the server.
	httpServer.RegisterOnShutdown(tm.Close)

	var listener net.Listener
	if cfg.SocketPath != "" {
		if err := removeStaleSocket(cfg.SocketPath); err != nil {
			log.Fatalf("tamarackdb-server: %v", err)
		}
		listener, err = net.Listen("unix", cfg.SocketPath)
		if err == nil {
			// The socket is created with the umask's permissions, which let
			// no other user connect: connecting takes write permission.
			if err := os.Chmod(cfg.SocketPath, cfg.SocketFileMode()); err != nil {
				log.Fatalf("tamarackdb-server: set socketMode on %s: %v", cfg.SocketPath, err)
			}
		}
	} else {
		listener, err = net.Listen("tcp", net.JoinHostPort(cfg.BindAddress, strconv.Itoa(cfg.Port)))
	}
	if err != nil {
		log.Fatalf("tamarackdb-server: %v", err)
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.SocketPath != "" {
		log.Printf("tamarackdb-server: listening on %s (auth=%t)", cfg.SocketPath, cfg.EnableAuth)
	} else {
		scheme := "http"
		if cfg.EnableTLS {
			scheme = "https"
		}
		addr := listener.Addr().(*net.TCPAddr)
		host := addr.IP.String()
		if addr.IP.IsUnspecified() {
			host = "localhost" // 0.0.0.0 or :: is not a useful link target
		}
		log.Printf("tamarackdb-server: listening on %s://%s (auth=%t)", scheme, net.JoinHostPort(host, strconv.Itoa(addr.Port)), cfg.EnableAuth)
	}

	serveErrCh := make(chan error, 1)
	if cfg.SocketPath == "" && cfg.EnableTLS {
		go func() { serveErrCh <- httpServer.ServeTLS(listener, cfg.TLSCertFile, cfg.TLSKeyFile) }()
	} else {
		go func() { serveErrCh <- httpServer.Serve(listener) }()
	}

	optimizeTicker := time.NewTicker(optimizeInterval)
	defer optimizeTicker.Stop()
	go func() {
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-optimizeTicker.C:
				if err := tm.Optimize(signalCtx); err != nil {
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
	tm.Close() // already started by Shutdown; waits for the active transaction's rollback
	if err := st.Close(); err != nil {
		log.Printf("tamarackdb-server: store close error: %v", err)
	}
	os.Exit(exitCode)
}

// removeStaleSocket removes a unix socket left behind by an earlier run,
// so the server can listen on path again. It refuses to remove anything
// that isn't a socket, so a socketPath set to a regular file or a
// directory by mistake is never deleted.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("check socket %s: %w", path, err)
	case info.Mode()&os.ModeSocket == 0:
		return fmt.Errorf("socketPath %s exists and is not a unix socket, refusing to remove it", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	return nil
}

// defaultConfigTemplate is a starter [server] TOML configuration, meant to
// be piped into a file and adjusted. It spells out every key, including the
// ones config.Load would otherwise default on its own, so this is a
// complete reference of what's configurable rather than a partial file.
// Every key is commented out at its default value, and uncommenting a line
// is how it takes effect. Uncommenting bindAddress or port switches the
// server from socketPath to TCP, even at their default values. It is a
// hand-written template, not a Marshal of config.Config, so it can carry
// comments; TOML's Marshal would drop them.
const defaultConfigTemplate = `[server]
# socketPath = "%s"
# socketMode = "%s"
# bindAddress = "%s"
# port = %d
# enableTls = false
# tlsCertFile = "/path/to/cert.pem"
# tlsKeyFile = "/path/to/key.pem"
# enableAuth = false
# authToken = "changeme"
# dataDir = "%s"
# devMode = false
# logLevel = "%s"
# defaultEventsPerPage = %d
# maxEventsPerPage = %d
# maxEventSize = %d # bytes
# maxProjectionSize = %d # bytes
# maxProjectionsPerRequest = %d
# transactionTimeout = %d # seconds
# maxTransactionDuration = %d # seconds
# maxQueuedTransactions = %d
# readPoolSize = %d
`

// printDefaultConfig writes defaultConfigTemplate to stdout, with its
// defaults filled in from the config package's exported constants.
func printDefaultConfig() {
	fmt.Printf(defaultConfigTemplate,
		config.DefaultSocketPath, config.DefaultSocketMode, config.DefaultBindAddress, config.DefaultPort, config.DefaultDataDir,
		config.DefaultLogLevel,
		config.DefaultEventsPerPage, config.DefaultMaxEventsPerPage, config.DefaultEventSize,
		config.DefaultProjectionSize, config.DefaultMaxProjectionsPerRequest,
		config.DefaultTransactionTimeout, config.DefaultMaxTransactionDuration,
		config.DefaultMaxQueuedTransactions, config.DefaultReadPoolSize)
}
