package subagentbatch

import (
	"context"
	"strings"
	"time"
)

// TurnSuspension is the §6.12 parked-turn record: the durable evidence that a
// parent turn handed work to background obligations and parked itself instead
// of blocking. The design names exactly these fields
// {turn_id, session_id, obligation_ids, parked_at, decision_window_until,
// resume_queue}; runtime-only bookkeeping (root scope for the wake plane) is
// carried alongside so a restart can re-enter the same supervision scope.
//
// The record is only meaningful on a durable store: on an in-memory store it
// dies with the process, which is why callers must gate on BatchStore.IsDurable
// before writing it (design §6.13 I9).
type TurnSuspension struct {
	// TurnID identifies the parked parent turn.
	TurnID string
	// SessionID is the parent session that owns the turn.
	SessionID string
	// RootScopeID groups every turn of one supervision scope (normally the
	// session id; kept explicit so a restart re-enters the same wake scope).
	RootScopeID string
	// ObligationIDs are the obligations dispatched before parking. Batch-backed
	// obligations stay bare ids (matching ResumeQueue); child agent sessions
	// dispatched by spawn_agent carry the AgentSessionObligationPrefix so both
	// kinds share one persisted representation and no schema migration.
	ObligationIDs []string
	// ParkedAt is when the turn parked.
	ParkedAt time.Time
	// DecisionWindowUntil is the escalate-first decision deadline for the
	// parked obligations; zero means "no window recorded".
	DecisionWindowUntil time.Time
	// ResumeQueue holds the wake keys that will drive the resume; ordering is
	// the delivery order and duplicates are preserved.
	ResumeQueue []string
}

// Clone returns a deep copy so callers cannot mutate stored slices.
func (t *TurnSuspension) Clone() *TurnSuspension {
	if t == nil {
		return nil
	}
	out := *t
	out.ObligationIDs = append([]string(nil), t.ObligationIDs...)
	out.ResumeQueue = append([]string(nil), t.ResumeQueue...)
	return &out
}

// normalize trims identifiers and drops empty obligation/queue entries so the
// primary key and the JSON payload cannot disagree on "same record".
func (t *TurnSuspension) normalize() {
	if t == nil {
		return
	}
	t.TurnID = strings.TrimSpace(t.TurnID)
	t.SessionID = strings.TrimSpace(t.SessionID)
	t.RootScopeID = strings.TrimSpace(t.RootScopeID)
	if t.RootScopeID == "" {
		t.RootScopeID = t.SessionID
	}
	t.ObligationIDs = normalizeIDList(t.ObligationIDs)
	t.ResumeQueue = normalizeIDList(t.ResumeQueue)
}

func normalizeIDList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AgentSessionObligationPrefix marks an obligation backed by a child agent
// session (a lightweight spawn_agent child) instead of a subagent batch. The
// obligation stays a single string entry so the §6.12 record keeps exactly one
// persisted representation (obligation_ids_json) and needs no schema change;
// readers that only understand batches must skip prefixed ids.
const AgentSessionObligationPrefix = "agent_session:"

// AgentSessionObligationID renders the obligation id for one child session.
func AgentSessionObligationID(sessionID string) string {
	return AgentSessionObligationPrefix + strings.TrimSpace(sessionID)
}

// IsAgentSessionObligation reports whether an obligation id is backed by a
// child agent session.
func IsAgentSessionObligation(obligationID string) bool {
	return strings.HasPrefix(strings.TrimSpace(obligationID), AgentSessionObligationPrefix)
}

// AgentSessionIDFromObligation returns the child session id encoded in an
// agent-session obligation id ("" when the id is batch-backed).
func AgentSessionIDFromObligation(obligationID string) string {
	trimmed := strings.TrimSpace(obligationID)
	if !strings.HasPrefix(trimmed, AgentSessionObligationPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, AgentSessionObligationPrefix))
}

// ObligationAgentSessionIDs lists the child-session obligations in record
// order (batch-backed ids are ignored).
func (t *TurnSuspension) ObligationAgentSessionIDs() []string {
	if t == nil {
		return nil
	}
	obligations := normalizeIDList(t.ObligationIDs)
	if len(obligations) == 0 {
		return nil
	}
	var sessionIDs []string
	for _, obligation := range obligations {
		if sessionID := AgentSessionIDFromObligation(obligation); sessionID != "" {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}
	return sessionIDs
}

// TurnObligationsSettled reports whether every obligation referenced by the
// parked-turn record reached a terminal state, i.e. whether the parked turn may
// end (design §6.12 / EC-E1 "turn 永不结束").
//
// This is the batch-only entry point kept for callers that never write
// child-session obligations; it delegates to TurnObligationsSettledWith with a
// nil resolver (a record carrying agent_session: obligations therefore stays
// parked — conservative, never cleared on a guess). Callers that also park
// spawn_agent children must pass their durable child-session resolver via the
// With form.
func TurnObligationsSettled(ctx context.Context, store BatchStore, record *TurnSuspension) (bool, error) {
	return TurnObligationsSettledWith(ctx, store, record, nil)
}

// obligationBatchIDs returns the batch ids that gate this parked turn.
// ResumeQueue carries the batch wake keys written by the dispatcher
// (parkBackgroundTurn writes exactly [batch_id]); ObligationIDs[0] is the same
// batch id and only serves as a fallback for records written without a queue.
// ObligationBatchIDs exposes the same list to the abandon executor (EC-E7 /
// EC-C5): abandoning a parked turn must cascade-cancel exactly the obligations
// the settle check reads, so both paths share one resolution rule.
func (t *TurnSuspension) ObligationBatchIDs() []string {
	if t == nil {
		return nil
	}
	if ids := normalizeIDList(t.ResumeQueue); len(ids) > 0 {
		return ids
	}
	obligations := normalizeIDList(t.ObligationIDs)
	if len(obligations) == 0 {
		return nil
	}
	for _, obligation := range obligations {
		// Child-session obligations are not batches; the fallback exists for
		// batch ids written without a resume queue.
		if IsAgentSessionObligation(obligation) {
			continue
		}
		return []string{obligation}
	}
	return nil
}

func (t *TurnSuspension) obligationBatchIDs() []string {
	return t.ObligationBatchIDs()
}
