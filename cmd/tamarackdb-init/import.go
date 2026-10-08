package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// importBatch is how many events one store.Import call commits. A
// variable, so a test can lower it.
var importBatch = 10000

// summary is what an import wrote: the user compares it with the source.
type summary struct {
	count       int64
	first, last int64 // the first and last sequence
}

// importDump creates the database at path from the events of dump. It
// writes into a temporary file next to path, and gives it the name path
// only once every event is in: when the import stops, there is no
// database at path. It never replaces a file already at path.
func importDump(ctx context.Context, path string, dump io.Reader) (summary, error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".import-*")
	if err != nil {
		return summary{}, err
	}
	tmp := f.Name()
	defer removeTemp(tmp)
	if err := f.Close(); err != nil {
		return summary{}, err
	}

	st, err := store.Open(ctx, tmp, 0)
	if err != nil {
		return summary{}, err
	}
	sum, err := writeEvents(ctx, st, dump)
	if closeErr := st.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return summary{}, err
	}

	// Closing the last connection copies the WAL into the database file
	// and deletes it. The WAL is found by the database file's name, so
	// events left in it would be lost when the file gets its final name.
	if _, err := os.Stat(tmp + "-wal"); !errors.Is(err, os.ErrNotExist) {
		return summary{}, fmt.Errorf("%s-wal is still there after the import: the database file may lack events", tmp)
	}
	// link(2) fails if path exists, where rename(2) would replace it.
	if err := os.Link(tmp, path); err != nil {
		return summary{}, err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return summary{}, err
	}
	return sum, nil
}

// writeEvents writes the events of dump into st, importBatch at a time.
// Each sequence must follow the one before it. A dump with no event is an
// error.
func writeEvents(ctx context.Context, st *store.Store, dump io.Reader) (summary, error) {
	r := newDumpReader(dump)
	var sum summary
	batch := make([]dcb.Event, 0, importBatch)
	for {
		ev, err := r.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, err
		}
		if sum.count > 0 && ev.Sequence != sum.last+1 {
			return sum, fmt.Errorf("line %d: sequence %d follows %d, it must be %d", r.line, ev.Sequence, sum.last, sum.last+1)
		}
		if sum.count == 0 {
			sum.first = ev.Sequence
		}
		sum.last = ev.Sequence
		sum.count++

		batch = append(batch, ev)
		if len(batch) == importBatch {
			if err := st.Import(ctx, batch); err != nil {
				return sum, err
			}
			batch = batch[:0]
		}
	}
	if sum.count == 0 {
		return sum, errors.New("the dump holds no event")
	}
	if err := st.Import(ctx, batch); err != nil {
		return sum, err
	}
	return sum, nil
}

// removeTemp removes the temporary database file and the files SQLite and
// the store keep beside it. After a successful import, tmp is only a
// second name of the database file.
func removeTemp(tmp string) {
	for _, p := range []string{tmp, tmp + "-wal", tmp + "-shm", tmp + ".lock"} {
		_ = os.Remove(p)
	}
}

// syncDir flushes dir, so the database file's new name survives a power
// loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
