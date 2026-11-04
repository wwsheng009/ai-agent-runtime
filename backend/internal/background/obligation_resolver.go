package background

import (
	"context"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// BackgroundJobTerminal is the durable obligation read used when a required
// background job gates a parked parent turn. It is deliberately implemented on
// Manager (rather than in a host adapter) so every host resolves the same job
// row the execution path wrote.
//
// found=false means there is no durable row for the id. A required job always
// has one while it can still be resumed, so callers must treat a missing row as
// pending rather than as completion.
func (m *Manager) BackgroundJobTerminal(ctx context.Context, jobID string) (bool, bool, error) {
	if m == nil {
		return false, false, nil
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return false, false, nil
	}
	job, err := m.GetJob(ctx, jobID)
	if err != nil {
		if runtimeerrors.Is(err, runtimeerrors.ErrJobNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	if job == nil {
		return false, false, nil
	}
	return IsTerminalStatus(job.Status), true, nil
}

// JobStoreDurable reports whether required-job obligations can survive a
// process restart. The parent suspension record is durable, so parking a
// required job on an in-memory job store would leave an unresolvable obligation
// after a crash. Hosts gate required-job admission on this probe.
func (m *Manager) JobStoreDurable() bool {
	if m == nil || m.store == nil {
		return false
	}
	if probe, ok := m.store.(interface{ IsDurable() bool }); ok {
		return probe.IsDurable()
	}
	// Custom stores are assumed durable: only the built-in in-memory fallback
	// can be identified reliably, and rejecting every custom store would make
	// required jobs unusable in embedded hosts.
	return true
}
