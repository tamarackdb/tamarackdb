package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// eventLine returns a dump line for sequence seq.
func eventLine(seq int64) string {
	return fmt.Sprintf(`{"sequence":%d,"time":"2024-03-01T09:12:44.000000Z","type":"imported",`+
		`"identifiers":{"n":"%d"},"metadata":{},"payload":"p"}`, seq, seq)
}

// smallBatches makes an import commit 3 events at a time, for the length
// of the test.
func smallBatches(t *testing.T) {
	saved := importBatch
	importBatch = 3
	t.Cleanup(func() { importBatch = saved })
}

// dumpOf returns a dump of the sequences from first to last.
func dumpOf(first, last int64) string {
	var b strings.Builder
	for seq := first; seq <= last; seq++ {
		b.WriteString(eventLine(seq))
		b.WriteByte('\n')
	}
	return b.String()
}

// writeDump writes dump to a file outside dataDir, and returns its path.
func writeDump(t *testing.T, dump string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.ndjson")
	if err := os.WriteFile(path, []byte(dump), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func databasePath(dataDir string) string {
	cfg := config.Config{DataDir: dataDir}
	return cfg.DatabasePath()
}

// readLines reads every event of the database in dataDir back, as dump
// lines in the shape QUERY /events returns.
func readLines(t *testing.T, dataDir string) []string {
	t.Helper()
	st, err := store.Open(context.Background(), databasePath(dataDir), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	it, err := st.Read(context.Background(), store.ReadFilter{Query: dcb.QueryAll(), Limit: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	var lines []string
	for it.Next() {
		ev := it.Event()
		lines = append(lines, fmt.Sprintf(`{"sequence":%d,"time":%q,"type":%q,"identifiers":%s,"metadata":%s,"payload":%q}`,
			ev.Sequence, ev.Time, ev.Type, ev.Identifiers, ev.Metadata, ev.Payload))
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// dirEntries lists the names in dir.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestImportKeepsEveryEventAsInTheDump(t *testing.T) {
	lines := []string{
		`{"sequence":1000,"time":"2024-03-01T09:12:44.123456Z","type":"order-placed","identifiers":{"orderId":"o-1","sku":["a","b"]},"metadata":{"tenantId":"acme"},"payload":"{\"total\":10}"}`,
		`{"sequence":1001,"time":"2023-01-01T00:00:00.000000Z","type":"order-paid","identifiers":{},"metadata":{},"payload":""}`,
		`{"sequence":1002,"time":"2024-03-01T09:12:44.123456Z","type":"order-shipped","identifiers":{"orderId":"o-1"},"metadata":{},"payload":"é"}`,
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := run(context.Background(), dataDir, writeDump(t, strings.Join(lines, "\n"))); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	if names := dirEntries(t, dataDir); len(names) != 1 || names[0] != "tamarackdb.sqlite" {
		t.Errorf("data dir holds %v, want only tamarackdb.sqlite", names)
	}
	got := readLines(t, dataDir)
	if strings.Join(got, "\n") != strings.Join(lines, "\n") {
		t.Errorf("read back:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(lines, "\n"))
	}
}

func TestImportThenAppendFollowsTheLastSequence(t *testing.T) {
	dataDir := t.TempDir()
	if err := run(context.Background(), dataDir, writeDump(t, dumpOf(5, 9))); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	st, err := store.Open(context.Background(), databasePath(dataDir), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	res, err := st.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "next"}}, dcb.Now()), nil, projection.Writes{})
	if err != nil {
		t.Fatal(err)
	}
	if seq := res.Events[0].Sequence; seq != 10 {
		t.Errorf("Sequence = %d, want 10", seq)
	}
}

func TestImportSeveralBatches(t *testing.T) {
	smallBatches(t)
	dataDir := t.TempDir()
	const last = 3*3 + 2
	sum, err := importDump(context.Background(), databasePath(dataDir), strings.NewReader(dumpOf(1, last)))
	if err != nil {
		t.Fatalf("importDump() error = %v", err)
	}
	if sum != (summary{count: last, first: 1, last: last}) {
		t.Errorf("summary = %+v", sum)
	}
	if n := len(readLines(t, dataDir)); n != last {
		t.Errorf("read back %d events, want %d", n, last)
	}
}

func TestImportFailsAndLeavesNothing(t *testing.T) {
	cases := map[string]string{
		"gap":                       dumpOf(1, 3) + dumpOf(5, 6),
		"gap after the first batch": dumpOf(1, 5) + dumpOf(7, 7),
		"repeated sequence":         dumpOf(1, 3) + dumpOf(3, 3),
		"sequence going back":       dumpOf(5, 6) + dumpOf(1, 1),
		"empty dump":                "",
		"bad last line":             dumpOf(1, 3) + "{}\n",
	}
	smallBatches(t)
	for name, dump := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dataDir := filepath.Join(parent, "data")
			if err := run(context.Background(), dataDir, writeDump(t, dump)); err == nil {
				t.Fatal("run() error = nil, want one")
			}
			if names := dirEntries(t, parent); len(names) != 0 {
				t.Errorf("parent holds %v, want nothing (the data dir removed)", names)
			}
		})
	}
}

func TestImportFailureKeepsAnExistingDataDir(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "other"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), dataDir, writeDump(t, dumpOf(1, 1)+dumpOf(3, 3)))
	if err == nil || !strings.Contains(err.Error(), "line 2: sequence 3 follows 1") {
		t.Fatalf("run() error = %v, want the gap on line 2", err)
	}
	if names := dirEntries(t, dataDir); len(names) != 1 || names[0] != "other" {
		t.Errorf("data dir holds %v, want only other", names)
	}
}

func TestImportNeverReplacesADatabase(t *testing.T) {
	dataDir := t.TempDir()
	if err := run(context.Background(), dataDir, writeDump(t, dumpOf(1, 2))); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), dataDir, writeDump(t, dumpOf(1, 5))); err == nil {
		t.Fatal("second run() error = nil, want a refusal")
	}
	if n := len(readLines(t, dataDir)); n != 2 {
		t.Errorf("read back %d events, want the 2 of the first import", n)
	}

	// A database created during the import, after the first check.
	path := databasePath(t.TempDir())
	if err := os.WriteFile(path, []byte("not mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importDump(context.Background(), path, strings.NewReader(dumpOf(1, 1))); err == nil {
		t.Fatal("importDump() error = nil, want a refusal")
	}
	if b, _ := os.ReadFile(path); string(b) != "not mine" {
		t.Errorf("the file at path was replaced")
	}
	if names := dirEntries(t, filepath.Dir(path)); len(names) != 1 {
		t.Errorf("dir holds %v, want only the database file", names)
	}
}

func TestImportMissingDumpCreatesNothing(t *testing.T) {
	parent := t.TempDir()
	if err := run(context.Background(), filepath.Join(parent, "data"), filepath.Join(parent, "missing.ndjson")); err == nil {
		t.Fatal("run() error = nil, want one")
	}
	if names := dirEntries(t, parent); len(names) != 0 {
		t.Errorf("parent holds %v, want nothing", names)
	}
}
