package agentcontrol

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReconcileMode selects what a reconcile pass is allowed to do with the drift
// it finds (plan P2-9 方案 1).
type ReconcileMode string

const (
	// ReconcileModeObserve only reports: the durable registry is read and the
	// audit result is returned, nothing is written. This is the default so a
	// long-running deployment never closes rows an operator may still want to
	// inspect.
	ReconcileModeObserve ReconcileMode = "observe"
	// ReconcileModeEnforce converges the safely reconcilable findings:
	// ACTIVE_AGENT_SESSION_MISSING / _TERMINAL close the binding subtree (which
	// is what releases the active-thread quota), ACTIVE_AGENT_SESSION_STALE
	// marks it stale when the store supports that distinction. Everything else
	// stays observe-only.
	ReconcileModeEnforce ReconcileMode = "enforce"
)

// Reconcile action verbs reported per finding.
const (
	ReconcileActionClose     = "close"
	ReconcileActionMarkStale = "mark_stale"
	ReconcileActionSkip      = "skip"
)

// reconcileActionForCode maps an audit issue code to the convergence verb. The
// audit emits these codes only when the session side is already gone, terminal
// or stale (see AuditAgentSessionConsistency), so a convergence can never
// interrupt a live run or a session parked on approval/waiting-input: those
// snapshots are neither missing nor terminal and produce no issue at all.
var reconcileActionForCode = map[string]string{
	IssueActiveAgentSessionMissing:  ReconcileActionClose,
	IssueActiveAgentSessionTerminal: ReconcileActionClose,
	IssueActiveAgentSessionStale:    ReconcileActionMarkStale,
}

// ReconcileAction is one convergence decision (taken or skipped) for an audit
// finding. It is part of the host-visible report so `/debug` and the HTTP
// supervision surface can explain what a pass did.
type ReconcileAction struct {
	IssueCode     string `json:"issue_code"`
	AgentID       string `json:"agent_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	RootSessionID string `json:"root_session_id,omitempty"`
	AgentPath     string `json:"agent_path,omitempty"`
	Action        string `json:"action"`
	Reason        string `json:"reason,omitempty"`
	Rows          int64  `json:"rows,omitempty"`
}

// ReconcileReport is the result of one audit + convergence pass. IssueCount is
// the same number the read-only audit reports; Converged/Skipped describe what
// enforce mode did about it.
type ReconcileReport struct {
	Mode           string                  `json:"mode"`
	ReconciledAt   time.Time               `json:"reconciled_at"`
	RecordsChecked int                     `json:"records_checked"`
	ActiveChecked  int                     `json:"active_checked"`
	IssueCount     int                     `json:"issue_count"`
	Converged      int                     `json:"converged"`
	ConvergedRows  int64                   `json:"converged_rows"`
	Skipped        int                     `json:"skipped"`
	Actions        []ReconcileAction       `json:"actions,omitempty"`
	Issues         []ConsistencyAuditIssue `json:"issues,omitempty"`
	// Reclaim* fold the host eviction sweep (plan §P2-9 方案 3) into the same
	// report: in observe mode ReclaimCandidates is what an enforce pass would
	// release, in enforce mode Reclaimed/ReclaimedRows are what it did release.
	ReclaimCandidates int      `json:"reclaim_candidates,omitempty"`
	Reclaimed         int      `json:"reclaimed,omitempty"`
	ReclaimedRows     int64    `json:"reclaimed_rows,omitempty"`
	ReclaimFailed     int      `json:"reclaim_failed,omitempty"`
	ReclaimReasons    []string `json:"reclaim_reasons,omitempty"`
	ReclaimError      string   `json:"reclaim_error,omitempty"`
}

// ReconcileAgentSessionConsistency audits the durable identity graph and, in
// enforce mode, converges the findings that are provably safe to converge.
//
// Safety rules (all must hold before a row is touched):
//   - the finding code is in reconcileActionForCode;
//   - the input record still exists, is not already terminal and carries both
//     root session id and agent path (otherwise there is nothing to route);
//   - the store is available and implements the required writer.
//
// Convergence is idempotent: the audit skips closed records, so a second pass
// over the same input reports no issue and writes nothing. A store that does
// not implement AgentStaleMarker falls back to close for stale findings, which
// keeps the quota accounting correct at the cost of the diagnostics flag.
func ReconcileAgentSessionConsistency(ctx context.Context, store AgentRegistryStore, records []AgentRecord, lookup SessionBindingLookup, mode ReconcileMode) (ReconcileReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if mode != ReconcileModeEnforce {
		mode = ReconcileModeObserve
	}
	report := ReconcileReport{Mode: string(mode), ReconciledAt: time.Now().UTC()}

	audit, err := AuditAgentSessionConsistency(ctx, records, lookup)
	if err != nil {
		return report, err
	}
	report.RecordsChecked = audit.RecordsChecked
	report.ActiveChecked = audit.ActiveChecked
	report.IssueCount = audit.IssueCount
	report.Issues = audit.Issues

	if mode != ReconcileModeEnforce || len(audit.Issues) == 0 {
		return report, nil
	}

	byAgentID := make(map[string]AgentRecord, len(records))
	for _, raw := range records {
		record := raw.Normalize()
		if record.AgentID == "" {
			continue
		}
		byAgentID[record.AgentID] = record
	}
	staleMarker, _ := store.(AgentStaleMarker)
	convergedInPass := make(map[string]bool, len(audit.Issues))

	for _, issue := range audit.Issues {
		action := reconcileActionForCode[issue.Code]
		if action == "" {
			continue
		}
		decision := ReconcileAction{
			IssueCode: issue.Code,
			AgentID:   issue.AgentID,
			SessionID: issue.SessionID,
			Action:    action,
		}
		record, ok := byAgentID[issue.AgentID]
		if !ok || record.AgentID == "" {
			decision.Action = ReconcileActionSkip
			decision.Reason = "agent record is not part of the audited set"
			report.Skipped++
			report.Actions = append(report.Actions, decision)
			continue
		}
		decision.RootSessionID = record.RootSessionID
		decision.AgentPath = record.AgentPath
		if record.RootSessionID == "" || record.AgentPath == "" {
			decision.Action = ReconcileActionSkip
			decision.Reason = "record has no root session or agent path to route the convergence"
			report.Skipped++
			report.Actions = append(report.Actions, decision)
			continue
		}
		if store == nil {
			decision.Action = ReconcileActionSkip
			decision.Reason = "durable registry store is not configured"
			report.Skipped++
			report.Actions = append(report.Actions, decision)
			continue
		}
		if record.Closed() {
			// The record was converged between the audit read and this write.
			decision.Action = ReconcileActionSkip
			decision.Reason = "record is already terminal"
			report.Skipped++
			report.Actions = append(report.Actions, decision)
			continue
		}
		if convergedInPass[record.AgentID] {
			// One record can produce more than one finding (for example a
			// terminal session that is also stale). Converge it once.
			decision.Action = ReconcileActionSkip
			decision.Reason = "record already converged in this pass"
			report.Skipped++
			report.Actions = append(report.Actions, decision)
			continue
		}

		now := report.ReconciledAt
		var rows int64
		var writeErr error
		if action == ReconcileActionMarkStale && staleMarker != nil {
			rows, writeErr = staleMarker.MarkAgentControlAgentSubtreeStale(ctx, record.RootSessionID, record.AgentPath, now)
		} else {
			rows, writeErr = store.CloseAgentControlAgentSubtree(ctx, record.RootSessionID, record.AgentPath, now)
			decision.Action = ReconcileActionClose
		}
		if writeErr != nil {
			return report, fmt.Errorf("reconcile %s for agent %s: %w", decision.Action, record.AgentID, writeErr)
		}
		decision.Rows = rows
		convergedInPass[record.AgentID] = true
		report.Converged++
		report.ConvergedRows += rows
		report.Actions = append(report.Actions, decision)
	}
	return report, nil
}

// HasConvergenceIssues reports whether the report contains findings the
// reconcile pass is allowed to converge (used by hosts to decide whether the
// next pass needs enforce mode at all).
func (r ReconcileReport) HasConvergenceIssues() bool {
	for _, issue := range r.Issues {
		if _, ok := reconcileActionForCode[issue.Code]; ok {
			return true
		}
	}
	return false
}

// Summary renders the one-line diagnostic form used by `/debug` status lines.
func (r ReconcileReport) Summary() string {
	if strings.TrimSpace(r.Mode) == "" {
		return "reconcile=not_run"
	}
	parts := []string{
		"reconcile=" + r.Mode,
		fmt.Sprintf("consistency_issues=%d", r.IssueCount),
	}
	if r.Converged > 0 || strings.EqualFold(r.Mode, string(ReconcileModeEnforce)) {
		parts = append(parts, fmt.Sprintf("converged=%d", r.Converged))
	}
	if r.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("skipped=%d", r.Skipped))
	}
	if r.ReclaimCandidates > 0 {
		parts = append(parts, fmt.Sprintf("reclaim_candidates=%d", r.ReclaimCandidates))
	}
	if r.Reclaimed > 0 {
		parts = append(parts, fmt.Sprintf("reclaimed=%d", r.Reclaimed))
	}
	if r.ReclaimedRows > 0 {
		parts = append(parts, fmt.Sprintf("reclaimed_rows=%d", r.ReclaimedRows))
	}
	if r.ReclaimFailed > 0 {
		parts = append(parts, fmt.Sprintf("reclaim_failed=%d", r.ReclaimFailed))
	}
	if len(r.ReclaimReasons) > 0 {
		parts = append(parts, "reclaim_reasons="+strings.Join(r.ReclaimReasons, ","))
	}
	if errText := strings.TrimSpace(r.ReclaimError); errText != "" {
		parts = append(parts, "reclaim_error="+errText)
	}
	if !r.ReconciledAt.IsZero() {
		parts = append(parts, "last_reconcile="+r.ReconciledAt.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, " ")
}
