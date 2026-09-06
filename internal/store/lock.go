package store

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockSuffix names the advisory lock file kept alongside the database file
// (path + lockSuffix), held for the lifetime of the process.
const lockSuffix = ".lock"

// acquireLock takes an exclusive, non-blocking flock(2) on path+lockSuffix,
// creating the file if needed. It fails fast with ErrDatabaseLocked if
// another process already holds it, rather than opening the SQLite file
// underneath a second writer. The returned file must be kept open (and
// eventually released with releaseLock) for as long as the Store using
// dbPath is in use; the OS releases the lock automatically if the process
// dies, so no stale lock file can block a later start.
func acquireLock(dbPath string) (*os.File, error) {
	f, err := os.OpenFile(dbPath+lockSuffix, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, wrapf("open lock file", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return nil, ErrDatabaseLocked
		}
		return nil, wrapf("lock database file", err)
	}
	return f, nil
}

// releaseLock unlocks and closes a file returned by acquireLock.
func releaseLock(f *os.File) error {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return f.Close()
}
