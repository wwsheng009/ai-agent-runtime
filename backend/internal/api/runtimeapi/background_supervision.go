package runtimeapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// backgroundJobTerminalProjectionTimeout bounds the best-effort supervision
// projection so a slow store can never delay job terminal bookkeeping.
const backgroundJobTerminalProjectionTimeout = 5 * time.Second

// isTerminalBackgroundJobEventType is the API host's whitelist check. The
// disposition itself lives in the supervision package so every host produces
// the same inbox item and wake identity.
func isTerminalBackgroundJobEventType(eventType string) bool {
	return supervision.IsTerminalBackgroundJobStatus(eventType)
}

// projectBackgroundJobTerminal is the API host adapter: it resolves the owning
// session's root scope, enriches the projection with the job's command and
// event payload, then delegates to the host-neutral supervision projection.
// It is best-effort and asynchronous: failures never change the job outcome.
func (h *Handler) projectBackgroundJobTerminal(parent context.Context, event background.JobEvent, sessionID string) {
	jobID := strings.TrimSpace(event.JobID)
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || jobID == "" || sessionID == "" {
		return
	}
	store := h.getSupervisionStore()
	if store == nil {
		return
	}
	if !supervision.IsTerminalBackgroundJobStatus(event.Type) {
		return
	}

	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, backgroundJobTerminalProjectionTimeout)
	defer cancel()

	_, _ = supervision.ProjectBackgroundJobTerminal(ctx, store, h.getSupervisionWakeScheduler(), supervision.BackgroundJobTerminalInput{
		RootScopeID:           h.backgroundJobRootScope(ctx, sessionID),
		TargetParentSessionID: sessionID,
		JobID:                 jobID,
		Status:                event.Type,
		Command:               h.backgroundJobCommand(ctx, jobID),
		ExitCode:              backgroundJobPayloadString(event.Payload["exit_code"]),
		ErrorCode:             backgroundJobPayloadString(event.Payload["error_code"]),
		Message:               backgroundJobPayloadString(event.Payload["message"]),
		CancelSource:          backgroundJobPayloadString(event.Payload["cancel_source"]),
		Epoch:                 backgroundJobTerminalEpoch(event),
	})
}

// backgroundJobRootScope resolves the budget/inbox root for the owning
// session. A session lookup failure degrades to the session id itself, which
// keeps the projection usable in hosts without a session manager.
func (h *Handler) backgroundJobRootScope(ctx context.Context, sessionID string) string {
	if h != nil && h.sessionManager != nil {
		if session, err := h.sessionManager.Get(ctx, sessionID); err == nil && session != nil {
			if root := strings.TrimSpace(apiAgentRootSessionID(session, sessionID)); root != "" {
				return root
			}
		}
	}
	return sessionID
}

// backgroundJobTerminalEpoch is the stable per-transition identity used as the
// notification subject version and wake terminal epoch. The manager stamps
// every event with CreatedAt, so replaying the same event keeps the epoch while
// a genuinely new terminal transition (a rerun) gets a new one.
func backgroundJobTerminalEpoch(event background.JobEvent) int64 {
	if !event.CreatedAt.IsZero() {
		if epoch := event.CreatedAt.UTC().UnixNano(); epoch > 0 {
			return epoch
		}
	}
	return time.Now().UTC().UnixNano()
}

// backgroundJobCommand reads the job's command for the digest reason. The
// manager is cached on the handler and may not exist yet (or at all) in tests
// and embedded hosts; a missing manager just yields an empty command.
func (h *Handler) backgroundJobCommand(ctx context.Context, jobID string) string {
	if h == nil {
		return ""
	}
	h.backgroundMu.Lock()
	manager := h.backgroundManager
	h.backgroundMu.Unlock()
	if manager == nil {
		return ""
	}
	job, err := manager.GetJob(ctx, jobID)
	if err != nil || job == nil {
		return ""
	}
	return job.Command
}

func backgroundJobPayloadString(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", value)
}
