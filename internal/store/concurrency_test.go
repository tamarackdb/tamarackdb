package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// TestConcurrentAppendsNoConditions exercises Store.Append directly (no
// gatekeeper involved), confirming BEGIN IMMEDIATE alone serializes writers
// correctly: unique, gapless sequences and no unexpected errors.
func TestConcurrentAppendsNoConditions(t *testing.T) {
	s := openTestStore(t)
	const goroutines = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)
	seqCh := make(chan int64, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			got, err := s.Append(context.Background(), []dcb.EventData{{Type: "concurrent"}}, nil)
			if err != nil {
				t.Errorf("Append() error = %v", err)
				return
			}
			seqCh <- got[0].Sequence
		}()
	}
	wg.Wait()
	close(seqCh)

	seen := make(map[int64]bool)
	for seq := range seqCh {
		if seen[seq] {
			t.Errorf("duplicate sequence %d", seq)
		}
		seen[seq] = true
	}
	if len(seen) != goroutines {
		t.Fatalf("got %d unique sequences, want %d", len(seen), goroutines)
	}

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: goroutines + 1})
	if len(events) != goroutines {
		t.Errorf("Read() returned %d events, want %d", len(events), goroutines)
	}
}

// TestConcurrentAppendsConflictingCondition races two goroutines that both
// try to be first to write an event matching a shared FailIfEventsMatch
// query; exactly one must win.
func TestConcurrentAppendsConflictingCondition(t *testing.T) {
	s := openTestStore(t)
	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "lockId", Value: "shared"}}}})

	const attempts = 10
	var successes atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			cond := &dcb.AppendCondition{FailIfEventsMatch: &q}
			_, err := s.Append(context.Background(), []dcb.EventData{eventWithIdentifier("t", "lockId", "shared")}, cond)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrConcurrencyConflict):
				conflicts.Add(1)
			default:
				t.Errorf("Append() error = %v, want nil or ErrConcurrencyConflict", err)
			}
		}()
	}
	wg.Wait()

	if successes.Load() != 1 {
		t.Errorf("successes = %d, want exactly 1", successes.Load())
	}
	if conflicts.Load() != attempts-1 {
		t.Errorf("conflicts = %d, want %d", conflicts.Load(), attempts-1)
	}
}
