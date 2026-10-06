package ui

import "time"

// raceScaledDeadline returns base unchanged for normal test binaries, and a
// race-scaled budget when the binary was built with -race. The detector
// multiplies the cost of the planning → write → ack → resume loops, which
// previously pushed heavy integration tests past their anti-hang watchdogs and
// turned the -race acceptance gate red without any semantic failure. Scaling
// only the watchdog keeps every assertion intact; a genuinely stuck run still
// fails, just after a detector-adjusted budget.
func raceScaledDeadline(base time.Duration) time.Duration {
	if raceInstrumented {
		return base * 6
	}
	return base
}
