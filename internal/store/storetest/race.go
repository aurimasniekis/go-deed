//go:build race

package storetest

// The race detector slows SQLite-heavy code several times over; the guard scales so it still catches outliers.
const raceFactor = 5
