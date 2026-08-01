package team

import "time"

// TeammateIsStale reports whether a teammate is considered offline/stale as of
// the given time: either it has never been seen (zero heartbeat) or its last
// heartbeat is older than offlineAfter. The same evaluator is shared by the
// SweepTeammates API and supervisor loops so offline detection stays
// consistent across surfaces.
func TeammateIsStale(mate Teammate, asOf time.Time, offlineAfter time.Duration) bool {
	if offlineAfter <= 0 {
		return false
	}
	if mate.LastHeartbeat.IsZero() {
		return true
	}
	return asOf.Sub(mate.LastHeartbeat) >= offlineAfter
}
