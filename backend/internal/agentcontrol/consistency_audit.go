package agentcontrol

import (
	"context"
	"fmt"
	"strings"
)

// Consistency audit issue codes. They are shared by the read-only audit, the
// reconcile pass and host diagnostics (P2-9), so hosts can match on symbols
// instead of string literals.
const (
	IssueAgentSessionIDMissing      = "AGENT_SESSION_ID_MISSING"
	IssueSessionLookupUnavailable   = "SESSION_LOOKUP_UNAVAILABLE"
	IssueAgentSessionIDMismatch     = "AGENT_SESSION_ID_MISMATCH"
	IssueActiveAgentSessionMissing  = "ACTIVE_AGENT_SESSION_MISSING"
	IssueActiveAgentSessionTerminal = "ACTIVE_AGENT_SESSION_TERMINAL"
	IssueActiveAgentSessionStale    = "ACTIVE_AGENT_SESSION_STALE"
)

// SessionBindingSnapshot is the minimal session-side state required to audit
// the AgentControl identity graph. The runtime deliberately accepts this as a
// callback so the control plane does not depend on chat storage or provider
// semantics.
type SessionBindingSnapshot struct {
	SessionID string
	Exists    bool
	Closed    bool
	Stale     bool
	Status    string
}

type SessionBindingLookup func(context.Context, string) (SessionBindingSnapshot, error)

type ConsistencyAuditIssue struct {
	Code      string `json:"code"`
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Detail    string `json:"detail"`
}

type ConsistencyAuditReport struct {
	RecordsChecked int                     `json:"records_checked"`
	ActiveChecked  int                     `json:"active_checked"`
	IssueCount     int                     `json:"issue_count"`
	Issues         []ConsistencyAuditIssue `json:"issues,omitempty"`
}

// AuditAgentSessionConsistency compares durable AgentControl rows with the
// session store. It reports drift without mutating either store. Terminal rows
// are included because stale historical bindings are useful diagnostics.
func AuditAgentSessionConsistency(ctx context.Context, records []AgentRecord, lookup SessionBindingLookup) (ConsistencyAuditReport, error) {
	report := ConsistencyAuditReport{Issues: make([]ConsistencyAuditIssue, 0)}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, raw := range records {
		record := raw.Normalize()
		report.RecordsChecked++
		if record.SessionID == "" {
			report.Issues = append(report.Issues, ConsistencyAuditIssue{Code: IssueAgentSessionIDMissing, AgentID: record.AgentID, Detail: "agent record has no session binding"})
			continue
		}
		if record.Closed() {
			continue
		}
		report.ActiveChecked++
		if lookup == nil {
			report.Issues = append(report.Issues, ConsistencyAuditIssue{Code: IssueSessionLookupUnavailable, AgentID: record.AgentID, SessionID: record.SessionID, Detail: "session binding lookup is not configured"})
			continue
		}
		snapshot, err := lookup(ctx, record.SessionID)
		if err != nil {
			return report, fmt.Errorf("audit agent %s session %s: %w", record.AgentID, record.SessionID, err)
		}
		if !snapshot.Exists {
			report.Issues = append(report.Issues, ConsistencyAuditIssue{Code: IssueActiveAgentSessionMissing, AgentID: record.AgentID, SessionID: record.SessionID, Detail: "active AgentControl row references a missing session"})
			continue
		}
		if strings.TrimSpace(snapshot.SessionID) != "" && strings.TrimSpace(snapshot.SessionID) != record.SessionID {
			report.Issues = append(report.Issues, ConsistencyAuditIssue{Code: IssueAgentSessionIDMismatch, AgentID: record.AgentID, SessionID: record.SessionID, Detail: "session lookup returned a different session id"})
		}
		if snapshot.Closed || strings.EqualFold(strings.TrimSpace(snapshot.Status), "closed") || strings.EqualFold(strings.TrimSpace(snapshot.Status), "stopped") {
			report.Issues = append(report.Issues, ConsistencyAuditIssue{Code: IssueActiveAgentSessionTerminal, AgentID: record.AgentID, SessionID: record.SessionID, Detail: "active AgentControl row references a terminal session"})
		}
		if snapshot.Stale {
			report.Issues = append(report.Issues, ConsistencyAuditIssue{Code: IssueActiveAgentSessionStale, AgentID: record.AgentID, SessionID: record.SessionID, Detail: "active AgentControl row references a stale session"})
		}
	}
	report.IssueCount = len(report.Issues)
	return report, nil
}
