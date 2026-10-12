//go:build !race

package store

// raceEnabled is false without -race; see race_test.go.
const raceEnabled = false
