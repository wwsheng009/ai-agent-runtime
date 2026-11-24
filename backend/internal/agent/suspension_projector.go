package agent

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TurnSuspensionProjector is the host callback for a successful durable park.
//
// Dispatch and persistence are intentionally separate: a child/batch may reach
// a terminal state between those two operations. The terminal projection can
// only schedule a settlement wake after the suspension row exists, so the
// host must reconcile the row immediately after it is written. The callback is
// best-effort from the agent runtime's perspective; its error is never allowed
// to turn a successful dispatch into a failed tool call.
type TurnSuspensionProjector func(context.Context, *subagentbatch.TurnSuspension) error

// SetTurnSuspensionProjector installs the host adapter that repairs the
// terminal-before-park ordering window for all loop-owned suspension kinds
// (batch, team, and required background job).
func (a *Agent) SetTurnSuspensionProjector(projector TurnSuspensionProjector) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.turnSuspensionProjector = projector
	a.mu.Unlock()
}

func (a *Agent) turnSuspensionProjectorSnapshot() TurnSuspensionProjector {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.turnSuspensionProjector
}

// projectTurnSuspension invokes the host repair hook after the suspension is
// durable. Errors are deliberately returned to the caller only for
// observability; loop callers ignore them so a supervision outage never
// changes the already accepted dispatch result.
func (a *Agent) projectTurnSuspension(ctx context.Context, record *subagentbatch.TurnSuspension) error {
	if a == nil {
		return nil
	}
	projector := a.turnSuspensionProjectorSnapshot()
	if projector == nil || record == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return projector(ctx, record)
}
