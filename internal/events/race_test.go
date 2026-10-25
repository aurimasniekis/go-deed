//go:build race

package events_test

// raceEnabled skips tests whose SQLite workload the race detector slows past the transaction guard.
const raceEnabled = true
