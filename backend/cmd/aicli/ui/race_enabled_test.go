//go:build race

package ui

// raceInstrumented reports whether this test binary was built with -race.
// The race detector slows the controller→executor→session closed loops several
// times over, so heavy integration tests scale their wall-clock watchdogs via
// raceScaledDeadline; every semantic assertion stays unchanged.
const raceInstrumented = true
