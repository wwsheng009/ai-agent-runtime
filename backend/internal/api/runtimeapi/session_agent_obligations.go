package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// apiAgentSessionObligationResolver judges the durable state of one lightweight
// spawn_agent child session from the same supervision notification plane that
// drives the API host's auto-wake/resume path: ProjectAgentCompletion writes an
// agent_completed/agent_failed/agent_interrupted lifecycle row per child whose
// SupervisionState is terminated. Reading that row back (including resolved
// rows) keeps settle/ledger semantics aligned with the control plane that
// actually decides whether the child is done. This mirrors the CLI host's
// localAgentSessionObligationResolver (cmd/aicli/commands/
// chat_actor_agent_obligations.go) so both hosts answer "did the child finish"
// from the same evidence.
type apiAgentSessionObligationResolver struct {
	store supervision.Store
}

func (r apiAgentSessionObligationResolver) AgentSessionTerminal(ctx context.Context, sessionID string) (bool, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if r.store == nil || sessionID == "" {
		return false, false, nil
	}
	rows, err := r.store.ListNotifications(ctx, supervision.NotificationFilter{
		SubjectKind:     supervision.SubjectAgentSession,
		SubjectID:       sessionID,
		IncludeResolved: true,
		Limit:           32,
	})
	if err != nil {
		return false, false, err
	}
	if len(rows) == 0 {
		// No durable lifecycle row yet: the child has not reached a terminal
		// projection (a missing row is never evidence of completion).
		return false, false, nil
	}
	for _, row := range rows {
		if row.SupervisionState == supervision.SupervisionTerminated {
			return true, true, nil
		}
	}
	return false, true, nil
}

// agentSessionObligationResolver returns the durable child-session reader for
// this host, or nil when the supervision control plane is not wired (in which
// case child-session obligations are never parked: a record that could never be
// settled must not be written — design §6.13 I9, same rule as the CLI host).
func (h *Handler) agentSessionObligationResolver() subagentbatch.AgentSessionObligationResolver {
	if h == nil {
		return nil
	}
	store := h.getSupervisionStore()
	if store == nil {
		return nil
	}
	return apiAgentSessionObligationResolver{store: store}
}

// apiTurnIDForSession resolves the turn identity a spawn_agent child must be
// parked under. The broker call context carries the running loop's turn
// annotation on both the actor path and the direct /api/agent/chat path; when it
// is absent (e.g. detached/delegated callers) the parent's runtime state is the
// authoritative fallback: CurrentTurnID for a running turn, SuspendedTurnID for
// an already parked one. Empty means "no turn to park".
func (c *sessionAgentController) apiTurnIDForSession(ctx context.Context, sessionID string) string {
	if turnID := strings.TrimSpace(agent.TurnIDFromContext(ctx)); turnID != "" {
		return turnID
	}
	if c == nil || c.handler == nil {
		return ""
	}
	store := c.handler.getSessionRuntimeStore()
	if store == nil {
		return ""
	}
	state, err := store.LoadState(ctx, sessionID)
	if err != nil || state == nil {
		return ""
	}
	if turnID := strings.TrimSpace(state.CurrentTurnID); turnID != "" {
		return turnID
	}
	return strings.TrimSpace(state.SuspendedTurnID)
}

// parkAgentChildObligation records that the current parent turn handed work to
// one live spawn_agent child session and parks the turn on it (§6.12), mirroring
// the CLI host's parkLocalAgentChildObligation. The obligation id is
// prefix-encoded (agent_session:<session_id>) so it reuses the existing
// obligation_ids_json representation with no schema migration; settlement reads
// it back through TurnObligationsSettledWith and the wait ledger through
// BuildWaitLedgerWith.
//
// Best-effort by contract (runtimeapi style): a missing durable batch store, a
// missing child-session resolver, an unreadable turn id or a write error simply
// leaves the turn unparked — the child-completion wake still fires on the normal
// projection path, and this helper never fails or delays the spawn.
func (c *sessionAgentController) parkAgentChildObligation(ctx context.Context, parentSessionID, childSessionID string) {
	if c == nil || c.handler == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	if parentSessionID == "" || childSessionID == "" {
		return
	}
	if c.handler.agentSessionObligationResolver() == nil {
		// Without a durable judge the record could never settle; do not write
		// an unparkable suspension (I9 conservative direction). Checked before
		// the store accessor so an unwired host never lazy-creates a batch DB
		// just to discard the write.
		return
	}
	store := c.handler.getSubagentBatchStore()
	if store == nil || !store.IsDurable() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	turnID := c.apiTurnIDForSession(ctx, parentSessionID)
	if turnID == "" {
		return
	}
	record, _, err := store.GetTurnSuspension(ctx, parentSessionID, turnID)
	if err != nil {
		return
	}
	if record == nil {
		record = &subagentbatch.TurnSuspension{TurnID: turnID, SessionID: parentSessionID}
	}
	record.RootScopeID = firstNonEmptyString(strings.TrimSpace(record.RootScopeID), parentSessionID)
	obligationID := subagentbatch.AgentSessionObligationID(childSessionID)
	for _, existing := range record.ObligationIDs {
		if existing == obligationID {
			return
		}
	}
	record.ObligationIDs = append(record.ObligationIDs, obligationID)
	if record.ParkedAt.IsZero() {
		record.ParkedAt = time.Now().UTC()
	}
	// A failed write keeps the turn unparked instead of failing the spawn.
	_ = store.ParkTurnSuspension(ctx, record)
}
