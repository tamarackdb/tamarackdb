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
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/api"
	"github.com/tamarackdb/tamarackdb/internal/buildinfo"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/tx"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

// banner is printed to stdout on startup, generated with `figlet TamarackDB`
// (standard font).
const banner = " _____                                    _    ____  ____\n" +
	"|_   _|_ _ _ __ ___   __ _ _ __ __ _  ___| | _|  _ \\| __ )\n" +
	"  | |/ _` | '_ ` _ \\ / _` | '__/ _` |/ __| |/ / | | |  _ \\\n" +
	"  | | (_| | | | | | | (_| | | | (_| | (__|   <| |_| | |_) |\n" +
	"  |_|\\__,_|_| |_| |_|\\__,_|_|  \\__,_|\\___|_|\\_\\____/|____/\n"

// optimizeInterval is how often the server runs PRAGMA optimize against the
// write connection, through the FIFO (see writer.Writer.Optimize).
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
	fmt.Printf("enableAuth: %t\n", cfg.EnableAuth)
	fmt.Printf("dataDir: %s\n", cfg.DataDir)
	fmt.Printf("devMode: %t\n", cfg.DevMode)
	fmt.Printf("logLevel: %s\n", cfg.LogLevel)
	fmt.Printf("defaultEventsPerPage: %d\n", cfg.DefaultEventsPerPage)
	fmt.Printf("maxEventsPerPage: %d\n", cfg.MaxEventsPerPage)
	fmt.Printf("maxEventSize: %d\n", cfg.MaxEventSize)
	fmt.Printf("maxProjectionSize: %d\n", cfg.MaxProjectionSize)
	fmt.Printf("maxEventsPerWrite: %d\n", cfg.MaxEventsPerWrite)
	fmt.Printf("maxProjectionsPerWrite: %d\n", cfg.MaxProjectionsPerWrite)
	fmt.Printf("maxRequestBodySize: %d\n", cfg.MaxRequestBodySize)
	fmt.Printf("maxQueuedWrites: %d\n", cfg.MaxQueuedWrites)
	fmt.Printf("readPoolSize: %d\n", cfg.ReadPoolSize)
	fmt.Printf("txIdleTimeout: %d\n\n", cfg.TxIdleTimeout)

	if err := requireDatabase(*cfg); err != nil {
		log.Fatalf("tamarackdb-server: %v", err)
	}
	st, err := store.Open(context.Background(), cfg.DatabasePath(), cfg.ReadPoolSize)
	if err != nil {
		log.Fatalf("tamarackdb-server: %v", err)
	}
	// st.Close() is not deferred: shutdown is ordered explicitly below,
	// not left to main's return.

	wr := writer.New(st, writer.Config{MaxQueued: cfg.MaxQueuedWrites})
	txs := tx.New(st, wr, tx.Config{
		IdleTimeout:            time.Duration(cfg.TxIdleTimeout) * time.Second,
		MaxEventsPerWrite:      cfg.MaxEventsPerWrite,
		MaxProjectionsPerWrite: cfg.MaxProjectionsPerWrite,
	})

	fatalCh := make(chan error, 1)
	srv := api.New(wr, txs, st, api.Options{
		Version:                buildinfo.Version,
		EnableAuth:             cfg.EnableAuth,
		AuthToken:              cfg.AuthToken,
		DefaultEventsPerPage:   cfg.DefaultEventsPerPage,
		MaxEventsPerPage:       cfg.MaxEventsPerPage,
		MaxEventSize:           cfg.MaxEventSize,
		MaxProjectionSize:      cfg.MaxProjectionSize,
		MaxEventsPerWrite:      cfg.MaxEventsPerWrite,
		MaxProjectionsPerWrite: cfg.MaxProjectionsPerWrite,
		MaxRequestBodySize:     cfg.MaxRequestBodySize,
		DevMode:                cfg.DevMode,
		LogLevel:               cfg.LogLevel,
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
	// the FIFO only ends once it gets its turn. Closing the writer
	// as soon as Shutdown starts turns those requests away right away; a
	// write already running finishes.
	httpServer.RegisterOnShutdown(wr.Close)

	var listener net.Listener
	if cfg.SocketPath != "" {
		if err := removeStaleSocket(cfg.SocketPath); err != nil {
			log.Fatalf("tamarackdb-server: %v", err)
		}
		listener, err = listenUnix(cfg.SocketPath, cfg.SocketFileMode())
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
		addr := listener.Addr().(*net.TCPAddr)
		host := addr.IP.String()
		if addr.IP.IsUnspecified() {
			host = "localhost" // 0.0.0.0 or :: is not a useful link target
		}
		log.Printf("tamarackdb-server: listening on http://%s (auth=%t)", net.JoinHostPort(host, strconv.Itoa(addr.Port)), cfg.EnableAuth)
	}

	// Plain HTTP only, on the socket or over TCP: TLS is a reverse proxy's
	// job.
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- httpServer.Serve(listener) }()

	optimizeTicker := time.NewTicker(optimizeInterval)
	defer optimizeTicker.Stop()
	go func() {
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-optimizeTicker.C:
				if err := wr.Optimize(signalCtx); err != nil {
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
	wr.Close() // already started by Shutdown; a no-op by now
	txs.Close()
	if err := st.Close(); err != nil {
		log.Printf("tamarackdb-server: store close error: %v", err)
	}
	os.Exit(exitCode)
}

// listenUnix listens on a unix socket at path, then sets its permissions
// to mode. Connecting to a unix socket takes write permission on it, so its
// permissions decide who may connect. The umask is set to 0177 while the
// socket is created, so it starts out as 0600: with a looser umask, such as
// the common 002, another user could otherwise connect in the moment
// before mode is applied. Changing the umask affects the whole process, so
// this runs at startup, before any other goroutine creates files.
func listenUnix(path string, mode os.FileMode) (net.Listener, error) {
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		// The default socketPath sits in /run/tamarackdb, which only exists
		// once systemd's RuntimeDirectory, or an operator, creates it. Say
		// so, instead of a bare "bind: no such file or directory".
		dir := filepath.Dir(path)
		if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("socket directory %s does not exist: create it, owned by the server's user, or set socketPath", dir)
		}
		return nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		l.Close()
		return nil, fmt.Errorf("set socketMode on %s: %w", path, err)
	}
	return l, nil
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
# enableAuth = false
# authToken = "changeme"
# dataDir = "%s"
# devMode = false
# logLevel = "%s"
# defaultEventsPerPage = %d
# maxEventsPerPage = %d
# maxEventSize = %d # bytes
# maxProjectionSize = %d # bytes
# maxEventsPerWrite = %d
# maxProjectionsPerWrite = %d
# maxRequestBodySize = %d # bytes
# maxQueuedWrites = %d
# readPoolSize = %d
# txIdleTimeout = %d # seconds
`

// printDefaultConfig writes defaultConfigTemplate to stdout, with its
// defaults filled in from the config package's exported constants.
func printDefaultConfig() {
	fmt.Printf(defaultConfigTemplate,
		config.DefaultSocketPath, config.DefaultSocketMode, config.DefaultBindAddress, config.DefaultPort, config.DefaultDataDir,
		config.DefaultLogLevel,
		config.DefaultEventsPerPage, config.DefaultMaxEventsPerPage, config.DefaultEventSize,
		config.DefaultProjectionSize,
		config.DefaultMaxEventsPerWrite, config.DefaultMaxProjectionsPerWrite, config.DefaultMaxRequestBodySize,
		config.DefaultMaxQueuedWrites, config.DefaultReadPoolSize, config.DefaultTxIdleTimeout)
}

// requireDatabase checks that the database file already exists: only
// tamarackdb-init creates it. A missing file most likely means a wrong
// dataDir or a disk that isn't mounted, and serving a new, empty store
// there would split the history in two without anyone noticing.
func requireDatabase(cfg config.Config) error {
	path := cfg.DatabasePath()
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s doesn't exist: check dataDir, or create a new database with tamarackdb-init --data-dir %s",
			path, cfg.DataDir)
	}
	return err
}
