package supervision

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// WakeReasonObligationSettled is the wake reason for a parked turn whose
// obligations all reached a terminal state without any of them being critical.
//
// The lifecycle projection only schedules a wake for critical + action-required
// rows (projection.go), so an all-success cohort — the happy path of the
// spawn_agent contract ("ending the turn is safe: the host parks the turn and
// auto-resumes it on the child's terminal event") — would otherwise leave the
// turn parked forever: the settle predicate runs at run end, and by then the
// run is already gone. This reason carries that missing edge. It deliberately
// contains no failure/approval keyword, so WakeBudgetClassOf lands it in the
// bounded "other" class and it shares the existing admission/budget machinery.
const WakeReasonObligationSettled = "obligation_settled"

// LifecycleWakeScheduled reports whether ProjectLifecycle scheduled an
// auto-wake for this notification (critical severity that still needs a parent
// decision). Callers use it to avoid scheduling a second, redundant settlement
// wake when the critical path already covers the same terminal event.
func LifecycleWakeScheduled(notification Notification) bool {
	return notification.Severity == SeverityCritical && notification.ActionRequired()
}

// ScheduleSettledTurnWake schedules one durable settlement wake when the parent
// session owns a parked turn whose obligations are all *known* terminal
// (TurnObligationsAllTerminal: a child without a lifecycle row still counts as
// running). It is called right after a child-completion projection that did not
// itself schedule a lifecycle wake; the caller then drains the wake through the
// normal MaybeWakeParent path.
//
// Best-effort and read-only: an unwired batch store, a missing resolver or an
// unreadable row simply leaves the turn parked (the previous behavior). The
// notify key is derived from the parked turn id, so repeated calls for the same
// turn collapse into one delivered wake even when several children finish in
// sequence.
func ScheduleSettledTurnWake(
	ctx context.Context,
	batches subagentbatch.BatchStore,
	resolver subagentbatch.AgentSessionObligationResolver,
	scheduler *WakeScheduler,
	parentSessionID, rootScopeID string,
) (bool, error) {
	if batches == nil || scheduler == nil {
		return false, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return false, nil
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	if rootScopeID == "" {
		rootScopeID = parentSessionID
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records, err := batches.ListTurnSuspensions(ctx, parentSessionID)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if record == nil {
			continue
		}
		settled, err := subagentbatch.TurnObligationsAllTerminal(ctx, batches, record, resolver)
		if err != nil {
			// One unreadable record must not hide a later settled one; the
			// turn keeps its previous (parked) behavior.
			continue
		}
		if !settled {
			continue
		}
		if _, err := scheduler.ScheduleWake(ctx, WakeRequest{
			RootScopeID:           rootScopeID,
			TargetParentSessionID: parentSessionID,
			WakeReason:            WakeReasonObligationSettled,
			TurnID:                record.TurnID,
			EventKind:             WakeEventLifecycle,
		}); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
