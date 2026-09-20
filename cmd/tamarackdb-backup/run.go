package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// maxNDJSONLine is the largest single NDJSON line (the hasMore trailer, or
// one event) this tool accepts from the source. It has no visibility into
// the source's own maxEventSize setting, so this is generous rather than
// tied to any particular configuration.
const maxNDJSONLine = 16 * 1024 * 1024

// readTrailer is the last NDJSON line of every QUERY /events response. Its
// absence (the stream ends without one) means the source cut the page
// short partway through; fetchPage treats that as an error rather than
// silently returning a partial page as if it were complete.
type readTrailer struct {
	HasMore bool `json:"hasMore"`
}

func run(ctx context.Context, configPath string) error {
	cfg, err := config.LoadBackup(configPath)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, cfg.DatabasePath, 0)
	if err != nil {
		return err
	}
	defer st.Close()

	lastSeq := st.LastSequence()
	var imported int
	for {
		events, hasMore, err := fetchPage(ctx, cfg, lastSeq)
		if err != nil {
			return err
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

	log.Printf("tamarackdb-backup: imported %d event(s), local sequence now %d", imported, lastSeq)
	return nil
}

// fetchPage issues one QUERY /events request against the source for every
// event after afterSeq, up to cfg.PageLimit events, and decodes the NDJSON
// response.
func fetchPage(ctx context.Context, cfg *config.BackupConfig, afterSeq int64) ([]dcb.Event, bool, error) {
	body, err := json.Marshal(struct {
		Query         string `json:"query"`
		AfterSequence int64  `json:"afterSequence"`
		Limit         int    `json:"limit"`
	}{Query: "*", AfterSequence: afterSeq, Limit: cfg.PageLimit})
	if err != nil {
		return nil, false, fmt.Errorf("tamarackdb-backup: build read request: %w", err)
	}

	url := strings.TrimRight(cfg.SourceURL, "/") + "/events"
	req, err := http.NewRequestWithContext(ctx, "QUERY", url, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("tamarackdb-backup: build read request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.SourceToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.SourceToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("tamarackdb-backup: read from %s: %w", cfg.SourceURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, false, fmt.Errorf("tamarackdb-backup: read from %s: unexpected status %s: %s", cfg.SourceURL, resp.Status, snippet)
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
			return nil, false, fmt.Errorf("tamarackdb-backup: parse event: %w", err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("tamarackdb-backup: read response body: %w", err)
	}
	if trailer == nil {
		return nil, false, fmt.Errorf("tamarackdb-backup: read %s: response ended before the page finished (no trailing hasMore line); retry", cfg.SourceURL)
	}

	return events, trailer.HasMore, nil
}
