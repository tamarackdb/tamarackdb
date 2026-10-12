//go:build race

package store

// raceEnabled is true under go test -race. A volume test skips itself
// then: it starts no goroutine, so the race detector has nothing to find
// in it, and the instrumented SQLite makes it run for minutes.
const raceEnabled = true
