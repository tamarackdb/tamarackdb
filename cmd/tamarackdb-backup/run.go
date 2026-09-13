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

// maxNDJSONLine is the largest single NDJSON line (the hasMore header, or
// one event) this tool accepts from the source. It has no visibility into
// the source's own maxEventSize setting, so this is generous rather than
// tied to any particular configuration.
const maxNDJSONLine = 16 * 1024 * 1024

// readHeader is the first NDJSON line of every QUERY /read response.
type readHeader struct {
	HasMore bool `json:"hasMore"`
}

func run(ctx context.Context, configPath string) error {
	cfg, err := config.LoadBackup(configPath)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, cfg.DatabasePath)
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

// fetchPage issues one QUERY /read request against the source for every
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

	url := strings.TrimRight(cfg.SourceURL, "/") + "/read"
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

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, false, fmt.Errorf("tamarackdb-backup: read response header: %w", err)
		}
		return nil, false, fmt.Errorf("tamarackdb-backup: read response header: empty response body")
	}
	var header readHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return nil, false, fmt.Errorf("tamarackdb-backup: parse response header: %w", err)
	}

	var events []dcb.Event
	for scanner.Scan() {
		var ev dcb.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			return nil, false, fmt.Errorf("tamarackdb-backup: parse event: %w", err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("tamarackdb-backup: read response body: %w", err)
	}

	return events, header.HasMore, nil
}
