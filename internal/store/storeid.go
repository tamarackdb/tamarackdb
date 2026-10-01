package store

import (
	"context"

	"github.com/google/uuid"
)

// The store ID names the events and projections held in a database file.
// It's a UUID, kept in the single row of the store table: drawn when the
// file is created, and drawn again by Reset, which empties the file. Two
// reads that return the same store ID read the same history, so a client
// can compare it to know whether a Sequence Position it kept still means
// anything.

func newStoreID() string {
	return uuid.NewString()
}

func readStoreID(ctx context.Context, q querier) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, "SELECT id FROM store").Scan(&id)
	return id, wrapf("read store id", err)
}
