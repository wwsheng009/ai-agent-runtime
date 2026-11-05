package subagentbatch

import (
	"context"
	"strings"
	"time"
)

// FinalizationPolicy declares whether an obligation blocks the parent turn's
// finalization or only produces a late notification once the turn is done.
// The zero value is treated as required for compatibility with every existing
// batch / child-session / team obligation.
type FinalizationPolicy string

const (
	FinalizationPolicyRequired         FinalizationPolicy = "required"
	FinalizationPolicyDetached         FinalizationPolicy = "detached"
	FinalizationPolicyExplicitFollowup FinalizationPolicy = "explicit_followup"
)

// NormalizeFinalizationPolicy canonicalizes a policy value. Empty and unknown
// values fall back to required: an untyped old obligation must keep blocking
// the parent turn rather than silently becoming detached.
func NormalizeFinalizationPolicy(value FinalizationPolicy) FinalizationPolicy {
	switch FinalizationPolicy(strings.ToLower(strings.TrimSpace(string(value)))) {
	case FinalizationPolicyDetached:
		return FinalizationPolicyDetached
	case FinalizationPolicyExplicitFollowup:
		return FinalizationPolicyExplicitFollowup
	default:
		return FinalizationPolicyRequired
	}
}

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
	// EpisodeID identifies the logical episode of the parked turn. Automatic
	// resumes reuse this id; a user follow-up after finalization starts a new
	// episode. Empty is a legacy record and can only be notified, never
	// automatically resumed.
	EpisodeID string
	// SessionID is the parent session that owns the turn.
	SessionID string
	// RootScopeID groups every turn of one supervision scope (normally the
	// session id; kept explicit so a restart re-enters the same wake scope).
	RootScopeID string
	// ObligationIDs are the obligations dispatched before parking. Batch-backed
	// obligations stay bare ids (matching ResumeQueue); child agent sessions
	// dispatched by subagent_spawn carry the AgentSessionObligationPrefix so both
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
	// ObligationPolicies records a non-required policy per obligation. A
	// missing entry means required, which keeps every existing dispatch path
	// blocking finalization without a schema data migration.
	ObligationPolicies map[string]FinalizationPolicy
	// ResultsConsumed records that the parent run has already received the
	// child-result payload for this park epoch (for example through
	// subagent_wait). Settlement must not clear a child-result obligation before
	// this flag is true, otherwise a deadline/crash between child completion and
	// synthesis could finalize the turn with the results unread.
	//
	// Missing/false is the safe default for newly parked records. A new
	// obligation in the same epoch resets it to false.
	ResultsConsumed bool `json:"results_consumed,omitempty"`
}

// Clone returns a deep copy so callers cannot mutate stored slices.
func (t *TurnSuspension) Clone() *TurnSuspension {
	if t == nil {
		return nil
	}
	out := *t
	out.EpisodeID = strings.TrimSpace(t.EpisodeID)
	out.ObligationIDs = append([]string(nil), t.ObligationIDs...)
	out.ResumeQueue = append([]string(nil), t.ResumeQueue...)
	if len(t.ObligationPolicies) > 0 {
		out.ObligationPolicies = make(map[string]FinalizationPolicy, len(t.ObligationPolicies))
		for id, policy := range t.ObligationPolicies {
			out.ObligationPolicies[id] = NormalizeFinalizationPolicy(policy)
		}
	}
	return &out
}

// normalize trims identifiers and drops empty obligation/queue entries so the
// primary key and the JSON payload cannot disagree on "same record".
func (t *TurnSuspension) normalize() {
	if t == nil {
		return
	}
	t.TurnID = strings.TrimSpace(t.TurnID)
	t.EpisodeID = strings.TrimSpace(t.EpisodeID)
	t.SessionID = strings.TrimSpace(t.SessionID)
	t.RootScopeID = strings.TrimSpace(t.RootScopeID)
	if t.RootScopeID == "" {
		t.RootScopeID = t.SessionID
	}
	t.ObligationIDs = normalizeIDList(t.ObligationIDs)
	t.ResumeQueue = normalizeIDList(t.ResumeQueue)
	if len(t.ObligationPolicies) == 0 {
		t.ObligationPolicies = nil
		return
	}
	policies := make(map[string]FinalizationPolicy, len(t.ObligationPolicies))
	for id, policy := range t.ObligationPolicies {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		normalized := NormalizeFinalizationPolicy(policy)
		if normalized == FinalizationPolicyRequired {
			// Required is the implicit default. Keeping it out of the map keeps
			// old and new records byte-identical for existing dispatch paths.
			continue
		}
		policies[id] = normalized
	}
	if len(policies) == 0 {
		t.ObligationPolicies = nil
		return
	}
	t.ObligationPolicies = policies
}

// PolicyForObligation returns the declared policy for one obligation. Missing
// entries are required, including every obligation written before policies
// existed.
func (t *TurnSuspension) PolicyForObligation(obligationID string) FinalizationPolicy {
	if t == nil {
		return FinalizationPolicyRequired
	}
	obligationID = strings.TrimSpace(obligationID)
	if policy, ok := t.ObligationPolicies[obligationID]; ok {
		return NormalizeFinalizationPolicy(policy)
	}
	return FinalizationPolicyRequired
}

// RequiredObligationIDs returns only the obligations that block finalization.
// Detached / explicit-followup entries stay on the record for evidence but do
// not gate the parent turn.
func (t *TurnSuspension) RequiredObligationIDs() []string {
	if t == nil {
		return nil
	}
	ids := normalizeIDList(t.ObligationIDs)
	if len(ids) == 0 {
		return nil
	}
	required := make([]string, 0, len(ids))
	for _, id := range ids {
		if t.PolicyForObligation(id) != FinalizationPolicyRequired {
			continue
		}
		required = append(required, id)
	}
	return required
}

// HasChildResults reports whether this park epoch owns a result-producing
// obligation (subagent batch or lightweight agent session). Team and background
// job obligations are intentionally excluded: their completion is delivered by
// their own control-plane payload and has no SynthesisPacket contract.
func (t *TurnSuspension) HasChildResults() bool {
	if t == nil {
		return false
	}
	for _, obligationID := range normalizeIDList(t.ObligationIDs) {
		if IsAgentSessionObligation(obligationID) || strings.HasPrefix(obligationID, "batch") {
			return true
		}
	}
	return false
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
// session (a lightweight subagent_spawn child) instead of a subagent batch. The
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

// SubjectIDFromObligation maps an obligation id to the supervision subject id
// it gates: batch ids stay as-is; agent-session/team/background-job ids lose
// their transport prefix. Unknown ids are returned trimmed so callers can still
// match a future obligation family without silently dropping it.
func SubjectIDFromObligation(obligationID string) string {
	trimmed := strings.TrimSpace(obligationID)
	switch {
	case IsAgentSessionObligation(trimmed):
		return AgentSessionIDFromObligation(trimmed)
	case IsTeamObligation(trimmed):
		return TeamIDFromObligation(trimmed)
	case IsBackgroundJobObligation(trimmed):
		return BackgroundJobIDFromObligation(trimmed)
	default:
		return trimmed
	}
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

// TeamObligationPrefix marks an obligation backed by a durable team run
// (team_spawn with auto_start) instead of a subagent batch or a child session.
// It follows the same single-string representation as
// AgentSessionObligationPrefix: one persisted form (obligation_ids_json), no
// schema migration, and readers that only understand batches must skip
// prefixed ids.
const TeamObligationPrefix = "team:"

// TeamObligationID renders the obligation id for one team run.
func TeamObligationID(teamID string) string {
	return TeamObligationPrefix + strings.TrimSpace(teamID)
}

// IsTeamObligation reports whether an obligation id is backed by a team run.
func IsTeamObligation(obligationID string) bool {
	return strings.HasPrefix(strings.TrimSpace(obligationID), TeamObligationPrefix)
}

// TeamIDFromObligation returns the team id encoded in a team obligation id
// ("" when the id is batch- or child-session-backed).
func TeamIDFromObligation(obligationID string) string {
	trimmed := strings.TrimSpace(obligationID)
	if !strings.HasPrefix(trimmed, TeamObligationPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, TeamObligationPrefix))
}

// ObligationTeamIDs lists the team-run obligations in record order
// (batch- and child-session-backed ids are ignored).
func (t *TurnSuspension) ObligationTeamIDs() []string {
	if t == nil {
		return nil
	}
	obligations := normalizeIDList(t.ObligationIDs)
	if len(obligations) == 0 {
		return nil
	}
	var teamIDs []string
	for _, obligation := range obligations {
		if teamID := TeamIDFromObligation(obligation); teamID != "" {
			teamIDs = append(teamIDs, teamID)
		}
	}
	return teamIDs
}

// BackgroundJobObligationPrefix marks an obligation backed by a durable
// background job (job_start with finalization_policy=required). The job lives
// in the background manager/store, not in the batch table, so the settle and
// resume predicates must ask the host for its durable state. The prefix keeps
// one persisted representation (obligation_ids_json) and prevents a job id from
// being mistaken for a batch id.
const BackgroundJobObligationPrefix = "job:"

// BackgroundJobObligationID renders the obligation id for one background job.
func BackgroundJobObligationID(jobID string) string {
	return BackgroundJobObligationPrefix + strings.TrimSpace(jobID)
}

// IsBackgroundJobObligation reports whether an obligation id is backed by a
// background job.
func IsBackgroundJobObligation(obligationID string) bool {
	return strings.HasPrefix(strings.TrimSpace(obligationID), BackgroundJobObligationPrefix)
}

// BackgroundJobIDFromObligation returns the job id encoded in a job obligation
// id ("" when the id is batch-, child-session-, or team-backed).
func BackgroundJobIDFromObligation(obligationID string) string {
	trimmed := strings.TrimSpace(obligationID)
	if !strings.HasPrefix(trimmed, BackgroundJobObligationPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, BackgroundJobObligationPrefix))
}

// ObligationBackgroundJobIDs lists the background-job obligations in record
// order (batch-, child-session-, and team-backed ids are ignored).
func (t *TurnSuspension) ObligationBackgroundJobIDs() []string {
	if t == nil {
		return nil
	}
	obligations := normalizeIDList(t.ObligationIDs)
	if len(obligations) == 0 {
		return nil
	}
	var jobIDs []string
	for _, obligation := range obligations {
		if jobID := BackgroundJobIDFromObligation(obligation); jobID != "" {
			jobIDs = append(jobIDs, jobID)
		}
	}
	return jobIDs
}

// TurnObligationsSettled reports whether every obligation referenced by the
// parked-turn record reached a terminal state, i.e. whether the parked turn may
// end (design §6.12 / EC-E1 "turn 永不结束").
//
// This is the batch-only entry point kept for callers that never write
// child-session obligations; it delegates to TurnObligationsSettledWith with a
// nil resolver (a record carrying agent_session: obligations therefore stays
// parked — conservative, never cleared on a guess). Callers that also park
// subagent_spawn children must pass their durable child-session resolver via the
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
		// Child-session / team-run obligations are not batches; the fallback
		// exists for batch ids written without a resume queue.
		if IsAgentSessionObligation(obligation) || IsTeamObligation(obligation) || IsBackgroundJobObligation(obligation) {
			continue
		}
		return []string{obligation}
	}
	return nil
}

func (t *TurnSuspension) obligationBatchIDs() []string {
	return t.ObligationBatchIDs()
}
