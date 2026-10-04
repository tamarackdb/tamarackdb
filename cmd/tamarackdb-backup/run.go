package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// storeHeader carries the source's store ID on every QUERY /events
// response, empty pages included.
const storeHeader = "X-Tamarackdb-Store"

// maxNDJSONLine is the largest single NDJSON line (the hasMore trailer, or
// one event) this tool accepts from the source. It has no visibility into
// the source's own maxEventSize setting, so this is generous rather than
// tied to any particular configuration.
const maxNDJSONLine = 16 * 1024 * 1024

// fetchTimeout bounds one QUERY /events request, response body included. A
// source that stops answering would otherwise hang the run forever, holding
// the backup file's lock, and every later scheduled run would fail on it.
const fetchTimeout = 5 * time.Minute

// source is where pages are read from: the source instance's base URL, and
// the client that reaches it, over TCP or over a unix socket.
type source struct {
	client  *http.Client
	baseURL string
	name    string // for error messages
}

// newSource builds the source cfg names. With sourceSocket, every
// connection goes to the unix socket, whatever the URL's host: the base URL
// only supplies the scheme and the path.
func newSource(cfg *config.BackupConfig) source {
	if cfg.SourceSocket != "" {
		path := cfg.SourceSocket
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		}
		return source{
			client:  &http.Client{Timeout: fetchTimeout, Transport: transport},
			baseURL: "http://tamarackdb",
			name:    "unix socket " + path,
		}
	}
	return source{
		client:  &http.Client{Timeout: fetchTimeout},
		baseURL: strings.TrimRight(cfg.SourceURL, "/"),
		name:    cfg.SourceURL,
	}
}

// readTrailer is the last NDJSON line of every QUERY /events response. Its
// absence (the stream ends without one) means the source cut the page
// short partway through; fetchPage treats that as an error rather than
// silently returning a partial page as if it were complete.
type readTrailer struct {
	HasMore bool `json:"hasMore"`
}

// run copies the events the backup file doesn't have yet. The file is
// named after the source's store ID, so the events of one store never land
// in the file of another: the store ID is asked first, then that file is
// opened or created, and every page read after must carry the same ID.
func run(ctx context.Context, configPath string) error {
	cfg, err := config.LoadBackup(configPath)
	if err != nil {
		return err
	}
	src := newSource(cfg)

	// The first event of the source, read only for the store ID its page
	// carries: the event itself is dropped. The page is empty for an empty
	// source, and carries the store ID all the same.
	_, _, storeID, err := fetchPage(ctx, src, cfg, 0, 1)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	path := filepath.Join(cfg.DataDir, storeID+".sqlite")
	st, err := store.Open(ctx, path, 0)
	if err != nil {
		return err
	}
	defer st.Close()

	lastSeq := st.LastSequence()
	var imported int
	for {
		events, hasMore, pageStoreID, err := fetchPage(ctx, src, cfg, lastSeq, cfg.PageLimit)
		if err != nil {
			return err
		}
		if pageStoreID != storeID {
			return fmt.Errorf("the source's store ID changed from %s to %s during the run (a reset): "+
				"nothing of this page was imported, the next run starts a new backup file", storeID, pageStoreID)
		}
		if len(events) > 0 {
			if err := st.Import(ctx, events); err != nil {
				return err
			}
			lastSeq = events[len(events)-1].Sequence
			imported += len(events)
		}
		if !hasMore {
			break
		}
	}

	log.Printf("tamarackdb-backup: imported %d event(s) into %s, local sequence now %d", imported, path, lastSeq)
	return nil
}

// fetchPage issues one QUERY /events request against the source for every
// event after afterSeq, up to limit events, and decodes the NDJSON
// response. It also returns the store ID the page was read on, once it's
// checked to be a UUID in its canonical form: it ends up in a file name,
// and must never be able to point outside the backup directory.
func fetchPage(ctx context.Context, src source, cfg *config.BackupConfig, afterSeq int64, limit int) ([]dcb.Event, bool, string, error) {
	body, err := json.Marshal(struct {
		Query         string `json:"query"`
		AfterSequence int64  `json:"afterSequence"`
		Limit         int    `json:"limit"`
	}{Query: "all", AfterSequence: afterSeq, Limit: limit})
	if err != nil {
		return nil, false, "", fmt.Errorf("build read request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "QUERY", src.baseURL+"/events", bytes.NewReader(body))
	if err != nil {
		return nil, false, "", fmt.Errorf("build read request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.SourceToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.SourceToken)
	}

	resp, err := src.client.Do(req)
	if err != nil {
		return nil, false, "", fmt.Errorf("read from %s: %w", src.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, false, "", fmt.Errorf("read from %s: unexpected status %s: %s", src.name, resp.Status, snippet)
	}
	storeID := resp.Header.Get(storeHeader)
	if id, err := uuid.Parse(storeID); err != nil || id.String() != storeID {
		return nil, false, "", fmt.Errorf("read from %s: %s header is %q, want a store ID (a UUID)", src.name, storeHeader, storeID)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxNDJSONLine)

	// The trailer can only be told apart from an event line by shape, not
	// position: it's the last line, but that's only known once the stream
	// ends. Every line is probed for a "hasMore" key rather than assumed
	// to be at a fixed offset.
	var events []dcb.Event
	var trailer *readTrailer
	for scanner.Scan() {
		line := scanner.Bytes()
		var probe struct {
			HasMore *bool `json:"hasMore"`
		}
		if err := json.Unmarshal(line, &probe); err == nil && probe.HasMore != nil {
			trailer = &readTrailer{HasMore: *probe.HasMore}
			continue
		}
		var ev dcb.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, false, "", fmt.Errorf("parse event: %w", err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, "", fmt.Errorf("read response body: %w", err)
	}
	if trailer == nil {
		return nil, false, "", fmt.Errorf("read %s: response ended before the page finished (no trailing hasMore line); retry", src.name)
	}

	return events, trailer.HasMore, storeID, nil
}
