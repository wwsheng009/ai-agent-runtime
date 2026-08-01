package team

import (
	"errors"
	"time"
)

// ErrOrchestratorLeaseHeld reports that another runtime instance currently
// holds a valid orchestrator owner lease for the team. The caller must not
// start claiming tasks and should back off instead of retrying hot.
var ErrOrchestratorLeaseHeld = errors.New("orchestrator owner lease is held by another instance")

// ErrOrchestratorLeaseLost reports that this instance's owner lease expired or
// was taken over (fencing token mismatch). The caller must stop claiming tasks
// and applying terminal state immediately; a newer owner has taken over.
var ErrOrchestratorLeaseLost = errors.New("orchestrator owner lease lost")

// DefaultOrchestratorLeaseTTL is the default lease duration for team
// orchestrator ownership. A crashed owner stops renewing and is considered
// dead after this window plus the renewal interval (TTL/3).
const DefaultOrchestratorLeaseTTL = 15 * time.Second

// OrchestratorLease is the durable single-owner lease for a team's
// orchestrator loop. Exactly one runtime instance may hold a valid lease for
// a team at any time; the fencing token rotates on every (re)acquisition so a
// stale owner can never claim tasks or apply terminal state after losing the
// lease.
type OrchestratorLease struct {
	TeamID               string
	OwnerID              string
	OwnerInstance        string
	LeaseUntil           time.Time
	FencingToken         string
	HeartbeatAt          time.Time
	LastTickAt           *time.Time
	LastSuccessfulTickAt *time.Time
	RestartCount         int
	LastError            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}
