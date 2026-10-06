//go:build !race

package ui

// raceInstrumented reports whether this test binary was built with -race.
// See race_enabled_test.go for the -race variant.
const raceInstrumented = false
