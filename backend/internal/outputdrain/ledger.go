// Package outputdrain defines the shared, versioned terminal-output drain
// ledger used by background jobs and batch task result capsules.
package outputdrain

import "strings"

// Phase is the versioned lifecycle of one terminal-output boundary.
type Phase string

const (
	// PhaseAbsorbing means the producer is still allowed to append output.
	PhaseAbsorbing Phase = "absorbing"
	// PhaseDraining means the terminal transition is closing the boundary.
	PhaseDraining Phase = "draining"
	// PhaseDrained means the stable result boundary is established.
	PhaseDrained Phase = "drained"
	// PhaseLateDiscarded means a write arrived after PhaseDrained and was
	// deliberately dropped rather than attached to the stable result.
	PhaseLateDiscarded Phase = "late_discarded"
)

const (
	// ConflictDiscardLate is used by background jobs: late producer bytes are
	// counted and discarded, never allowed to overwrite the settled result.
	ConflictDiscardLate = "discard_late"
	// ConflictRejectTerminalWrite is used by batch task capsules: the task
	// store rejects a terminal overwrite outright.
	ConflictRejectTerminalWrite = "reject_terminal_write"
)

// Ledger is the durable drain accounting attached to one result boundary.
// Version starts at 1 and increments on every late-write decision.
type Ledger struct {
	Version        int64  `json:"version"`
	Phase          Phase  `json:"phase"`
	TerminalEpoch  string `json:"terminal_epoch,omitempty"`
	DrainedAt      string `json:"drained_at,omitempty"`
	AbsorbedBytes  int64  `json:"absorbed_bytes,omitempty"`
	DrainedBytes   int64  `json:"drained_bytes,omitempty"`
	LateBytes      int64  `json:"late_bytes,omitempty"`
	LateDiscarded  bool   `json:"late_discarded,omitempty"`
	ConflictPolicy string `json:"conflict_policy,omitempty"`
}

// Normalize fills the monotonic defaults without changing a valid ledger.
func (l Ledger) Normalize() Ledger {
	l.Phase = Phase(strings.TrimSpace(string(l.Phase)))
	if l.Version <= 0 {
		l.Version = 1
	}
	if l.Phase == "" {
		l.Phase = PhaseDrained
	}
	if l.AbsorbedBytes < 0 {
		l.AbsorbedBytes = 0
	}
	if l.DrainedBytes < 0 {
		l.DrainedBytes = 0
	}
	if l.LateBytes < 0 {
		l.LateBytes = 0
	}
	if l.LateBytes > 0 {
		l.LateDiscarded = true
	}
	l.ConflictPolicy = strings.TrimSpace(l.ConflictPolicy)
	if l.ConflictPolicy == "" {
		l.ConflictPolicy = ConflictDiscardLate
	}
	return l
}

// AddLate increments the version and late-byte counter. It returns the new
// ledger; callers must persist it before considering the observation durable.
func (l Ledger) AddLate(bytes int64) Ledger {
	l = l.Normalize()
	if bytes <= 0 {
		return l
	}
	l.Version++
	l.Phase = PhaseLateDiscarded
	l.LateBytes += bytes
	l.LateDiscarded = true
	return l
}

// Metrics is the aggregate readout of drain ledgers.
type Metrics struct {
	Records          int            `json:"records"`
	Versions         int64          `json:"versions"`
	AbsorbedBytes    int64          `json:"absorbed_bytes"`
	DrainedBytes     int64          `json:"drained_bytes"`
	LateBytes        int64          `json:"late_bytes"`
	LateDiscarded    int            `json:"late_discarded"`
	ConflictPolicies map[string]int `json:"conflict_policies,omitempty"`
}

// Add folds one ledger into the aggregate.
func (m *Metrics) Add(l Ledger) {
	if m == nil {
		return
	}
	l = l.Normalize()
	m.Records++
	m.Versions += l.Version
	m.AbsorbedBytes += l.AbsorbedBytes
	m.DrainedBytes += l.DrainedBytes
	m.LateBytes += l.LateBytes
	if l.LateDiscarded {
		m.LateDiscarded++
	}
	if m.ConflictPolicies == nil {
		m.ConflictPolicies = map[string]int{}
	}
	m.ConflictPolicies[l.ConflictPolicy]++
}
