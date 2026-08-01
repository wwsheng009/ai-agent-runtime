package supervision

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// AlertCode identifies one operator-facing supervision alert class.
type AlertCode string

// P6-4 alert codes mapped from the doc 13.3 key-alert list:
//   - outbox backlog (completion delivery stuck)
//   - critical lifecycle notification unresolved too long
//   - child run with no progress (progress deadline passed)
//   - child run with expired owner lease (orphan suspected)
//   - parent wake pending but no runnable turn for a long time
const (
	AlertOutboxBacklog        AlertCode = "outbox_backlog"
	AlertCriticalStale        AlertCode = "critical_notification_stale"
	AlertRunProgressStalled   AlertCode = "run_progress_stalled"
	AlertRunOrphanSuspected   AlertCode = "run_orphan_suspected"
	AlertWakePendingStale     AlertCode = "wake_pending_stale"
)

// Alert is one evaluation output row. Alerts are derived read-only views over
// the durable store; they never mutate state.
type Alert struct {
	Code      AlertCode  `json:"code"`
	Severity  Severity   `json:"severity"`
	SubjectID string     `json:"subject_id,omitempty"`
	Message   string     `json:"message"`
	Age       time.Duration `json:"age,omitempty"`
	Count     int        `json:"count"`
}

// AlertConfig controls alert thresholds. Zero values fall back to the
// documented defaults via DefaultAlertConfig.
type AlertConfig struct {
	// OutboxBacklogMin is the minimum undelivered outbox count that alerts.
	OutboxBacklogMin int
	// OutboxStaleAge alerts when the oldest undelivered outbox entry is
	// older than this.
	OutboxStaleAge time.Duration
	// CriticalStaleAge alerts when a critical unresolved notification is
	// older than this.
	CriticalStaleAge time.Duration
	// WakeStaleAge alerts when an unclaimed wake pending row is older than
	// this.
	WakeStaleAge time.Duration
	// MaxAlerts caps the number of alerts returned per class (per run / per
	// notification). Zero means the default cap.
	MaxAlerts int
}

// DefaultAlertConfig returns the documented P6-4 thresholds.
func DefaultAlertConfig() AlertConfig {
	return AlertConfig{
		OutboxBacklogMin: 5,
		OutboxStaleAge:   2 * time.Minute,
		CriticalStaleAge: 2 * time.Minute,
		WakeStaleAge:     2 * time.Minute,
		MaxAlerts:        20,
	}
}

// EvaluateAlerts scans the durable supervision store and returns operator
// alerts (P6-4). rootScopeID narrows the scan; empty scans all scopes.
// The scan is best-effort: an unavailable sub-store (e.g. nil
// ExecutionRunStore) only skips that alert class.
func EvaluateAlerts(ctx context.Context, store Store, rootScopeID string, cfg AlertConfig) ([]Alert, error) {
	if store == nil {
		return nil, fmt.Errorf("supervision store is required")
	}
	cfg = cfg.withDefaults()
	now := time.Now().UTC()
	var alerts []Alert

	// --- completion outbox backlog ---
	runStore, ok := store.(ExecutionRunStore)
	if ok && runStore != nil {
		entries, err := runStore.ListUndeliveredOutbox(ctx, cfg.MaxAlerts+1)
		if err != nil {
			return nil, fmt.Errorf("list undelivered outbox: %w", err)
		}
		if len(entries) > 0 {
			oldest := entries[0]
			for _, e := range entries {
				if e.CreatedAt.Before(oldest.CreatedAt) {
					oldest = e
				}
			}
			age := now.Sub(oldest.CreatedAt)
			if len(entries) >= cfg.OutboxBacklogMin || age >= cfg.OutboxStaleAge {
				alerts = append(alerts, Alert{
					Code:      AlertOutboxBacklog,
					Severity:  SeverityWarning,
					SubjectID: oldest.RunID,
					Message:   fmt.Sprintf("completion outbox backlog: %d undelivered, oldest run=%s waiting %s", len(entries), oldest.RunID, age.Round(time.Second)),
					Age:       age,
					Count:     len(entries),
				})
			}
		}
	}

	// --- critical unresolved notifications ---
	filter := NotificationFilter{RootScopeID: rootScopeID, IncludeResolved: true}
	notifications, err := store.ListNotifications(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	sort.Slice(notifications, func(i, j int) bool {
		return notifications[i].CreatedAt.Before(notifications[j].CreatedAt)
	})
	criticalCount := 0
	var oldestCritical *Notification
	var oldestCriticalAge time.Duration
	for i := range notifications {
		n := notifications[i]
		if n.Severity != SeverityCritical || !n.Unresolved() {
			continue
		}
		criticalCount++
		age := now.Sub(n.CreatedAt)
		if oldestCritical == nil || n.CreatedAt.Before(oldestCritical.CreatedAt) {
			oldestCritical = &n
			oldestCriticalAge = age
		}
	}
	if oldestCritical != nil && oldestCriticalAge >= cfg.CriticalStaleAge {
		alerts = append(alerts, Alert{
			Code:      AlertCriticalStale,
			Severity:  SeverityCritical,
			SubjectID: oldestCritical.SubjectID,
			Message:   fmt.Sprintf("%d critical notification(s) unresolved, oldest subject=%s state=%s waiting %s", criticalCount, oldestCritical.SubjectID, oldestCritical.SupervisionState, oldestCriticalAge.Round(time.Second)),
			Age:       oldestCriticalAge,
			Count:     criticalCount,
		})
	}

	// --- active run stall / orphan ---
	if ok && runStore != nil {
		runs, err := runStore.ListActiveExecutionRuns(ctx, cfg.MaxAlerts*2)
		if err != nil {
			return nil, fmt.Errorf("list active runs: %w", err)
		}
		stallCount := 0
		orphanCount := 0
		var stallRun, orphanRun *ExecutionRun
		var stallAge, orphanAge time.Duration
		for i := range runs {
			run := runs[i]
			switch run.Status {
			case RunStatusCancelRequested, RunStatusCanceling,
				RunStatusSucceeded, RunStatusFailed, RunStatusCanceled,
				RunStatusTimedOut, RunStatusOrphaned, RunStatusSuperseded:
				continue
			}
			if run.ProgressDeadlineAt != nil && !run.ProgressDeadlineAt.IsZero() && now.After(*run.ProgressDeadlineAt) {
				stallCount++
				age := now.Sub(*run.ProgressDeadlineAt)
				if stallRun == nil || age > stallAge {
					stallRun = &run
					stallAge = age
				}
			} else if run.OwnerLeaseUntil != nil && !run.OwnerLeaseUntil.IsZero() && now.After(*run.OwnerLeaseUntil) {
				orphanCount++
				age := now.Sub(*run.OwnerLeaseUntil)
				if orphanRun == nil || age > orphanAge {
					orphanRun = &run
					orphanAge = age
				}
			}
		}
		if stallRun != nil {
			alerts = append(alerts, Alert{
				Code:      AlertRunProgressStalled,
				Severity:  SeverityWarning,
				SubjectID: stallRun.RunID,
				Message:   fmt.Sprintf("%d run(s) stalled without progress, oldest run=%s session=%s waiting %s", stallCount, stallRun.RunID, stallRun.SessionID, stallAge.Round(time.Second)),
				Age:       stallAge,
				Count:     stallCount,
			})
		}
		if orphanRun != nil {
			alerts = append(alerts, Alert{
				Code:      AlertRunOrphanSuspected,
				Severity:  SeverityCritical,
				SubjectID: orphanRun.RunID,
				Message:   fmt.Sprintf("%d run(s) with expired owner lease, oldest run=%s session=%s waiting %s", orphanCount, orphanRun.RunID, orphanRun.SessionID, orphanAge.Round(time.Second)),
				Age:       orphanAge,
				Count:     orphanCount,
			})
		}
	}

	// --- stale wake pending ---
	wakeFilter := WakeFilter{RootScopeID: rootScopeID, UnclaimedOnly: true, Limit: cfg.MaxAlerts}
	wakes, err := store.ListWakePending(ctx, wakeFilter)
	if err != nil {
		return nil, fmt.Errorf("list wake pending: %w", err)
	}
	wakeCount := 0
	var oldestWake *WakePending
	var wakeAge time.Duration
	for i := range wakes {
		w := wakes[i]
		wakeCount++
		age := now.Sub(w.CreatedAt)
		if oldestWake == nil || w.CreatedAt.Before(oldestWake.CreatedAt) {
			oldestWake = &w
			wakeAge = age
		}
	}
	if oldestWake != nil && wakeAge >= cfg.WakeStaleAge {
		alerts = append(alerts, Alert{
			Code:      AlertWakePendingStale,
			Severity:  SeverityWarning,
			SubjectID: oldestWake.TargetParentSessionID,
			Message:   fmt.Sprintf("%d wake_pending row(s) unclaimed, oldest parent=%s reason=%s waiting %s", wakeCount, oldestWake.TargetParentSessionID, oldestWake.WakeReason, wakeAge.Round(time.Second)),
			Age:       wakeAge,
			Count:     wakeCount,
		})
	}

	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Severity != alerts[j].Severity {
			return severityRank(alerts[i].Severity) < severityRank(alerts[j].Severity)
		}
		return alerts[i].Code < alerts[j].Code
	})
	if len(alerts) > cfg.MaxAlerts {
		alerts = alerts[:cfg.MaxAlerts]
	}
	return alerts, nil
}

func (c AlertConfig) withDefaults() AlertConfig {
	def := DefaultAlertConfig()
	if c.OutboxBacklogMin <= 0 {
		c.OutboxBacklogMin = def.OutboxBacklogMin
	}
	if c.OutboxStaleAge <= 0 {
		c.OutboxStaleAge = def.OutboxStaleAge
	}
	if c.CriticalStaleAge <= 0 {
		c.CriticalStaleAge = def.CriticalStaleAge
	}
	if c.WakeStaleAge <= 0 {
		c.WakeStaleAge = def.WakeStaleAge
	}
	if c.MaxAlerts <= 0 {
		c.MaxAlerts = def.MaxAlerts
	}
	return c
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}
