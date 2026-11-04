package supervision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// ResumeAdmissionDecision is the host-neutral answer to "may this wake start a
// model turn for the parent?". Only ResumeSameParkedTurn authorizes an
// automatic continuation; every late/detached/stale settlement is notification
// only.
type ResumeAdmissionDecision string

const (
	ResumeSameParkedTurn ResumeAdmissionDecision = "resume_same_parked_turn"
	NotifyOnly           ResumeAdmissionDecision = "notify_only"
	RejectStale          ResumeAdmissionDecision = "reject_stale"
	RetryAdmission       ResumeAdmissionDecision = "retry_admission"
	// ResumeLegacy is a P0 compatibility bridge for non-terminal supervision
	// wakes that do not carry a policy yet (progress/approval/self-check). P1
	// removes it once every automatic entry point declares a policy.
	ResumeLegacy ResumeAdmissionDecision = "resume_legacy"
)

// ParentTurnState is the durable logical state used by admission.
type ParentTurnState string

const (
	ParentTurnRunning   ParentTurnState = "running"
	ParentTurnParked    ParentTurnState = "parked"
	ParentTurnFinalized ParentTurnState = "finalized"
	ParentTurnCanceled  ParentTurnState = "canceled"
	ParentTurnUnknown   ParentTurnState = "unknown"
)

// ParentTurnSnapshot is the host-provided durable view of one parent session.
// It intentionally contains only scalar identity/phase fields so both CLI and
// API adapters can build it from their existing runtime state stores.
type ParentTurnSnapshot struct {
	SessionID          string
	Status             string
	CurrentTurnID      string
	CurrentEpisodeID   string
	SuspendedTurnID    string
	SuspendedEpisodeID string
	FinalizedTurnID    string
	FinalizedEpisodeID string
	Phase              ParentTurnState
}

// ParentTurnStateResolver reads the parent session's durable turn snapshot.
// Returning an error keeps the wake durable (RetryAdmission); it never falls
// back to "idle means resumable".
type ParentTurnStateResolver interface {
	ResolveParentTurn(ctx context.Context, sessionID string) (*ParentTurnSnapshot, error)
}

// ParentTurnSnapshotFunc adapts a host callback to ParentTurnStateResolver.
type ParentTurnSnapshotFunc func(ctx context.Context, sessionID string) (*ParentTurnSnapshot, error)

func (f ParentTurnSnapshotFunc) ResolveParentTurn(ctx context.Context, sessionID string) (*ParentTurnSnapshot, error) {
	if f == nil {
		return nil, fmt.Errorf("parent turn snapshot resolver is nil")
	}
	return f(ctx, sessionID)
}

// ResumeAdmissionInput is the durable identity of one wake.
type ResumeAdmissionInput struct {
	RootScopeID        string
	ParentSessionID    string
	ParentTurnID       string
	ParentEpisodeID    string
	ObligationID       string
	SettlementID       string
	SettlementCohortID string
	FinalizationPolicy string
	WakeID             string
	EventKind          string
	WakeReason         string
}

// ResumeAdmissionInputFromWake projects the durable wake row into the shared
// admission input. It is the one conversion used by both CLI and API paths.
func ResumeAdmissionInputFromWake(w WakePending) ResumeAdmissionInput {
	return normalizeResumeAdmissionInput(ResumeAdmissionInput{
		RootScopeID:        w.RootScopeID,
		ParentSessionID:    w.TargetParentSessionID,
		ParentTurnID:       firstNonEmptyString(strings.TrimSpace(w.ParentTurnID), strings.TrimSpace(w.TurnID)),
		ParentEpisodeID:    w.ParentEpisodeID,
		ObligationID:       w.ObligationID,
		SettlementID:       w.SettlementID,
		SettlementCohortID: w.SettlementCohortID,
		FinalizationPolicy: w.FinalizationPolicy,
		WakeID:             w.WakeID,
		EventKind:          w.EventKind,
		WakeReason:         w.WakeReason,
	})
}

// ResumeAdmissionResult includes the normalized identity and an optional
// suspension snapshot used by the delivery path.
type ResumeAdmissionResult struct {
	Decision            ResumeAdmissionDecision
	Reason              string
	Input               ResumeAdmissionInput
	Suspension          *subagentbatch.TurnSuspension
	RequiredObligations []string
	ClaimKey            string
}

// ResumeAdmissionConfig wires the shared admission service. Hosts must use the
// same service for CLI and API; only the parent-state adapter and delivery
// callback differ.
type ResumeAdmissionConfig struct {
	Suspensions    subagentbatch.BatchStore
	ParentStates   ParentTurnStateResolver
	AgentSessions  subagentbatch.AgentSessionObligationResolver
	Teams          subagentbatch.TeamObligationResolver
	BackgroundJobs subagentbatch.BackgroundJobObligationResolver
	Claims         Store
	Now            func() time.Time
	// ClaimStaleAfter bounds how long a `claimed` settlement may remain
	// unresolved before recovery converges it to submitted. Zero uses 5m.
	ClaimStaleAfter time.Duration
	// ClaimRetention bounds terminal claim history. Zero uses 7 days.
	ClaimRetention time.Duration
	// ClaimPruneInterval throttles opportunistic pruning from recovery drains.
	// Zero uses 1 hour.
	ClaimPruneInterval time.Duration
}

// ResumeAdmissionService is the single decision point for automatic resumes.
type ResumeAdmissionService struct {
	suspensions        subagentbatch.BatchStore
	parentStates       ParentTurnStateResolver
	agentSessions      subagentbatch.AgentSessionObligationResolver
	teams              subagentbatch.TeamObligationResolver
	backgroundJobs     subagentbatch.BackgroundJobObligationResolver
	claims             Store
	now                func() time.Time
	claimStaleAfter    time.Duration
	claimRetention     time.Duration
	claimPruneInterval time.Duration
	pruneMu            sync.Mutex
	lastPrune          time.Time

	localMu     sync.Mutex
	localClaims map[string]ResumeClaim
}

func NewResumeAdmissionService(cfg ResumeAdmissionConfig) *ResumeAdmissionService {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	staleAfter := cfg.ClaimStaleAfter
	if staleAfter <= 0 {
		staleAfter = 5 * time.Minute
	}
	retention := cfg.ClaimRetention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	pruneInterval := cfg.ClaimPruneInterval
	if pruneInterval <= 0 {
		pruneInterval = time.Hour
	}
	return &ResumeAdmissionService{
		suspensions:        cfg.Suspensions,
		parentStates:       cfg.ParentStates,
		agentSessions:      cfg.AgentSessions,
		teams:              cfg.Teams,
		backgroundJobs:     cfg.BackgroundJobs,
		claims:             cfg.Claims,
		now:                now,
		claimStaleAfter:    staleAfter,
		claimRetention:     retention,
		claimPruneInterval: pruneInterval,
		localClaims:        map[string]ResumeClaim{},
	}
}

// RecoverStaleClaims converges claims stuck in `claimed` past the configured
// lease. It is best-effort and safe to call from every drain: the UPDATE is
// idempotent and never retries a model turn.
func (s *ResumeAdmissionService) RecoverStaleClaims(ctx context.Context) (int64, error) {
	if s == nil || s.claims == nil {
		return 0, nil
	}
	cutoff := s.clock().Add(-s.claimStaleAfter)
	recovered, err := s.claims.RecoverStaleResumeClaims(ctx, cutoff)
	if err == nil {
		s.maybePruneClaims(ctx)
	}
	return recovered, err
}

// PruneClaims removes terminal claim history older than the retention window.
// It is safe to call from host maintenance; claimed rows are never removed.
func (s *ResumeAdmissionService) PruneClaims(ctx context.Context) (int64, error) {
	if s == nil || s.claims == nil {
		return 0, nil
	}
	return s.claims.PruneResumeClaims(ctx, s.clock().Add(-s.claimRetention))
}

func (s *ResumeAdmissionService) maybePruneClaims(ctx context.Context) {
	if s == nil || s.claims == nil {
		return
	}
	now := s.clock()
	s.pruneMu.Lock()
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < s.claimPruneInterval {
		s.pruneMu.Unlock()
		return
	}
	s.lastPrune = now
	s.pruneMu.Unlock()
	_, _ = s.PruneClaims(ctx)
}

// Evaluate applies the admission matrix without consuming the claim. Claim is a
// separate CAS step so a budget-deferred wake does not burn the settlement.
func (s *ResumeAdmissionService) Evaluate(ctx context.Context, in ResumeAdmissionInput) (ResumeAdmissionResult, error) {
	in = normalizeResumeAdmissionInput(in)
	result := ResumeAdmissionResult{Input: in, Decision: NotifyOnly}
	policy := normalizeAdmissionPolicy(in.FinalizationPolicy)
	turnIdentityProvided := strings.TrimSpace(in.ParentTurnID) != ""
	episodeIdentityProvided := strings.TrimSpace(in.ParentEpisodeID) != ""

	if policy == string(subagentbatch.FinalizationPolicyDetached) ||
		policy == string(subagentbatch.FinalizationPolicyExplicitFollowup) {
		result.Reason = "policy_not_resumable"
		return result, nil
	}

	// P0 compatibility bridge: only explicit policies and terminal/settlement
	// families are forced through admission. Non-terminal legacy supervision
	// wakes (progress/approval/self-check) keep their old behavior until P1.
	if policy == "" && !isTerminalAdmissionWake(in) {
		if s != nil && s.parentStates != nil {
			snapshot, err := s.parentStates.ResolveParentTurn(ctx, in.ParentSessionID)
			if err != nil {
				// An unreadable parent state must not be treated as an idle
				// invitation for a progress turn.
				result.Decision = RetryAdmission
				result.Reason = "parent_state_unreadable"
				return result, nil
			}
			if snapshot != nil {
				switch normalizeParentTurnState(snapshot.Phase) {
				case ParentTurnFinalized, ParentTurnCanceled:
					result.Reason = "parent_turn_finalized"
					return result, nil
				}
				if isBusyRuntimeStatus(snapshot.Status) {
					result.Decision = RetryAdmission
					result.Reason = "parent_turn_busy"
					return result, nil
				}
				if finalized := strings.TrimSpace(snapshot.FinalizedTurnID); finalized != "" &&
					finalized == strings.TrimSpace(in.ParentTurnID) {
					result.Reason = "parent_turn_finalized"
					return result, nil
				}
			}
		}
		result.Decision = ResumeLegacy
		result.Reason = "legacy_non_terminal"
		return result, nil
	}

	if s == nil {
		result.Decision = RetryAdmission
		result.Reason = "admission_service_unwired"
		return result, nil
	}
	if s.parentStates == nil {
		result.Decision = RetryAdmission
		result.Reason = "parent_state_resolver_unwired"
		return result, nil
	}
	snapshot, err := s.parentStates.ResolveParentTurn(ctx, in.ParentSessionID)
	if err != nil {
		result.Decision = RetryAdmission
		result.Reason = "parent_state_unreadable"
		return result, nil
	}
	if snapshot == nil {
		result.Decision = RetryAdmission
		result.Reason = "parent_state_missing"
		return result, nil
	}
	if isBusyRuntimeStatus(snapshot.Status) {
		result.Decision = RetryAdmission
		result.Reason = "parent_turn_busy"
		return result, nil
	}
	if phase := normalizeParentTurnState(snapshot.Phase); phase == ParentTurnFinalized || phase == ParentTurnCanceled {
		result.Reason = "parent_turn_" + string(phase)
		return result, nil
	}
	if s.suspensions == nil {
		result.Decision = RetryAdmission
		result.Reason = "suspension_store_unwired"
		return result, nil
	}
	record, resolveReason, err := s.resolveSuspension(ctx, in)
	if err != nil {
		result.Decision = RetryAdmission
		result.Reason = "suspension_unreadable"
		return result, nil
	}
	if record == nil {
		result.Reason = resolveReason
		return result, nil
	}
	if in.ParentTurnID == "" {
		in.ParentTurnID = strings.TrimSpace(record.TurnID)
		result.Input.ParentTurnID = in.ParentTurnID
	}
	if in.ParentEpisodeID == "" {
		if turnIdentityProvided && !episodeIdentityProvided {
			// The wake named a turn but no episode: that is a legacy/pre-identity
			// wake and must not be upgraded from whatever episode is parked now.
			result.Reason = "missing_parent_episode_identity"
			return result, nil
		}
		in.ParentEpisodeID = strings.TrimSpace(record.EpisodeID)
		result.Input.ParentEpisodeID = in.ParentEpisodeID
	}
	if strings.TrimSpace(in.SettlementCohortID) == "" && !record.ParkedAt.IsZero() {
		in.SettlementCohortID = settlementIdentity(record, record.ParkedAt.UTC().UnixNano())
		result.Input.SettlementCohortID = in.SettlementCohortID
	}
	if strings.TrimSpace(in.SettlementID) == "" {
		in.SettlementID = in.SettlementCohortID
		result.Input.SettlementID = in.SettlementID
	}
	if strings.TrimSpace(record.EpisodeID) == "" {
		result.Reason = "legacy_suspension_without_episode"
		return result, nil
	}
	if in.ParentEpisodeID == "" {
		result.Reason = "missing_parent_episode_identity"
		return result, nil
	}
	if in.ParentEpisodeID != strings.TrimSpace(record.EpisodeID) {
		result.Decision = RejectStale
		result.Reason = "episode_mismatch"
		return result, nil
	}
	if finalized := strings.TrimSpace(snapshot.FinalizedTurnID); finalized != "" && finalized == in.ParentTurnID {
		finalizedEpisode := strings.TrimSpace(snapshot.FinalizedEpisodeID)
		if finalizedEpisode == "" || in.ParentEpisodeID == "" || finalizedEpisode == in.ParentEpisodeID {
			result.Reason = "parent_turn_finalized"
			return result, nil
		}
	}
	required := record.RequiredObligationIDs()
	result.Suspension = record
	result.RequiredObligations = required
	if len(required) == 0 {
		result.Reason = "no_required_obligations"
		return result, nil
	}
	requiredRecord := record.Clone()
	requiredRecord.ObligationIDs = required
	requiredSet := make(map[string]bool, len(required))
	for _, id := range required {
		requiredSet[id] = true
	}
	requiredRecord.ResumeQueue = nil
	for _, id := range record.ResumeQueue {
		if requiredSet[strings.TrimSpace(id)] {
			requiredRecord.ResumeQueue = append(requiredRecord.ResumeQueue, id)
		}
	}
	settled, err := subagentbatch.TurnObligationsAllTerminalWithResolvers(ctx, s.suspensions, requiredRecord, subagentbatch.ObligationResolvers{
		AgentSessions:  s.agentSessions,
		Teams:          s.teams,
		BackgroundJobs: s.backgroundJobs,
	})
	if err != nil {
		result.Decision = RetryAdmission
		result.Reason = "obligations_unreadable"
		return result, nil
	}
	if !settled {
		result.Decision = RetryAdmission
		result.Reason = "required_obligations_pending"
		return result, nil
	}
	if snapshot.SuspendedTurnID == "" || strings.TrimSpace(snapshot.SuspendedTurnID) != in.ParentTurnID {
		result.Decision = RetryAdmission
		result.Reason = "parent_not_parked_for_turn"
		return result, nil
	}
	if suspendedEpisode := strings.TrimSpace(snapshot.SuspendedEpisodeID); suspendedEpisode != "" &&
		suspendedEpisode != in.ParentEpisodeID {
		result.Decision = RejectStale
		result.Reason = "runtime_episode_mismatch"
		return result, nil
	}
	result.ClaimKey = DeriveResumeClaimKey(in.ParentSessionID, in.ParentTurnID, in.ParentEpisodeID, in.SettlementCohortID, in.SettlementID)
	claim, claimErr := s.getClaim(ctx, result.ClaimKey)
	if claimErr != nil {
		result.Decision = RetryAdmission
		result.Reason = "resume_claim_unreadable"
		return result, nil
	}
	if claim != nil {
		switch claim.State {
		case ResumeClaimStateSubmitted:
			result.Decision = NotifyOnly
			result.Reason = "settlement_already_submitted"
			return result, nil
		case ResumeClaimStateClaimed:
			result.Decision = RetryAdmission
			result.Reason = "settlement_claimed"
			return result, nil
		}
	}
	result.Decision = ResumeSameParkedTurn
	result.Reason = "matching_parked_episode"
	return result, nil
}

// resolveSuspension finds the parked turn for a wake. The wake normally carries
// the turn explicitly; older host projections only carry the obligation/batch
// id, so admission derives the unique owning suspension from that durable
// relation. Ambiguity is retryable, never guessed.
func (s *ResumeAdmissionService) resolveSuspension(ctx context.Context, in ResumeAdmissionInput) (*subagentbatch.TurnSuspension, string, error) {
	if s == nil || s.suspensions == nil {
		return nil, "suspension_store_unwired", nil
	}
	if turnID := strings.TrimSpace(in.ParentTurnID); turnID != "" {
		record, ok, err := s.suspensions.GetTurnSuspension(ctx, in.ParentSessionID, turnID)
		if err != nil {
			return nil, "suspension_unreadable", err
		}
		if !ok || record == nil {
			return nil, "no_matching_parked_turn", nil
		}
		if obligationID := strings.TrimSpace(in.ObligationID); obligationID != "" && !suspensionHasObligation(record, obligationID) {
			return nil, "obligation_not_in_parked_turn", nil
		}
		return record, "", nil
	}
	obligationID := strings.TrimSpace(in.ObligationID)
	if obligationID == "" {
		return nil, "missing_parent_turn_identity", nil
	}
	records, err := s.suspensions.ListTurnSuspensions(ctx, in.ParentSessionID)
	if err != nil {
		return nil, "suspension_unreadable", err
	}
	var matched *subagentbatch.TurnSuspension
	for _, record := range records {
		if record == nil || !suspensionHasObligation(record, obligationID) {
			continue
		}
		if matched != nil && matched.TurnID != record.TurnID {
			return nil, "ambiguous_parked_turn", fmt.Errorf("obligation %s belongs to multiple parked turns", obligationID)
		}
		matched = record
	}
	if matched == nil {
		return nil, "no_matching_parked_turn", nil
	}
	return matched, "", nil
}

func suspensionHasObligation(record *subagentbatch.TurnSuspension, obligationID string) bool {
	if record == nil {
		return false
	}
	obligationID = strings.TrimSpace(obligationID)
	if obligationID == "" {
		return false
	}
	candidates := []string{
		obligationID,
		subagentbatch.AgentSessionObligationID(obligationID),
		subagentbatch.TeamObligationID(obligationID),
		subagentbatch.BackgroundJobObligationID(obligationID),
	}
	for _, existing := range record.ObligationIDs {
		for _, candidate := range candidates {
			if strings.TrimSpace(existing) == candidate {
				return true
			}
		}
	}
	return false
}

// Claim wins the durable exactly-once boundary for one settlement. A false
// return means another process/attempt already claimed or submitted it.
func (s *ResumeAdmissionService) Claim(ctx context.Context, result ResumeAdmissionResult) (ResumeClaim, bool, error) {
	if result.Decision != ResumeSameParkedTurn {
		return ResumeClaim{}, false, nil
	}
	claimKey := strings.TrimSpace(result.ClaimKey)
	if claimKey == "" {
		claimKey = DeriveResumeClaimKey(result.Input.ParentSessionID, result.Input.ParentTurnID, result.Input.ParentEpisodeID, result.Input.SettlementCohortID, result.Input.SettlementID)
	}
	claim := ResumeClaim{
		ClaimKey:           claimKey,
		RootScopeID:        result.Input.RootScopeID,
		ParentSessionID:    result.Input.ParentSessionID,
		ParentTurnID:       result.Input.ParentTurnID,
		ParentEpisodeID:    result.Input.ParentEpisodeID,
		SettlementID:       result.Input.SettlementID,
		SettlementCohortID: result.Input.SettlementCohortID,
		State:              ResumeClaimStateClaimed,
		ClaimedAt:          s.clock(),
	}
	if s != nil && s.claims != nil {
		return s.claims.ClaimResume(ctx, claim)
	}
	if s == nil {
		return ResumeClaim{}, false, fmt.Errorf("resume admission service is nil")
	}
	s.localMu.Lock()
	defer s.localMu.Unlock()
	if existing, ok := s.localClaims[claimKey]; ok {
		if existing.State == ResumeClaimStateReleased {
			existing.State = ResumeClaimStateClaimed
			existing.ClaimedAt = claim.ClaimedAt
			existing.SubmittedAt = nil
			existing.ModelTurnID = ""
			existing.DeliveryAttempt++
			existing.Version++
			s.localClaims[claimKey] = existing
			return existing, true, nil
		}
		return existing, false, nil
	}
	claim.Version = 1
	claim.DeliveryAttempt = 1
	s.localClaims[claimKey] = claim
	return claim, true, nil
}

func (s *ResumeAdmissionService) MarkSubmitted(ctx context.Context, claim ResumeClaim, modelTurnID string) error {
	if s == nil {
		return nil
	}
	at := s.clock()
	if s.claims != nil {
		_, err := s.claims.MarkResumeSubmitted(ctx, claim.ClaimKey, modelTurnID, at, claim.Version)
		return err
	}
	s.localMu.Lock()
	defer s.localMu.Unlock()
	stored := s.localClaims[claim.ClaimKey]
	if stored.State != ResumeClaimStateClaimed || stored.Version != claim.Version {
		return nil
	}
	stored.State = ResumeClaimStateSubmitted
	stored.SubmittedAt = &at
	stored.ModelTurnID = strings.TrimSpace(modelTurnID)
	stored.Version++
	s.localClaims[claim.ClaimKey] = stored
	return nil
}

// ClaimRenewInterval returns the heartbeat cadence for a live claimed row. It
// stays well inside ClaimStaleAfter so a slow model turn is never mistaken for
// a crashed delivery; short test/configuration windows get a proportionally
// short (but non-zero) interval.
func (s *ResumeAdmissionService) ClaimRenewInterval() time.Duration {
	if s == nil || s.claimStaleAfter <= 0 {
		return time.Minute
	}
	interval := s.claimStaleAfter / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval >= s.claimStaleAfter {
		interval = s.claimStaleAfter / 2
		if interval <= 0 {
			interval = s.claimStaleAfter
		}
	}
	return interval
}

// RenewClaim refreshes the lease timestamp of a claimed settlement while its
// delivery is still in flight. It is best-effort and intentionally does not
// bump Version: the holder keeps its original CAS token for MarkSubmitted /
// Release. A claim that already moved to submitted/released reports false.
func (s *ResumeAdmissionService) RenewClaim(ctx context.Context, claim ResumeClaim) (bool, error) {
	if s == nil {
		return false, nil
	}
	at := s.clock()
	if s.claims != nil {
		return s.claims.RenewResumeClaim(ctx, claim.ClaimKey, at, claim.Version)
	}
	s.localMu.Lock()
	defer s.localMu.Unlock()
	stored, ok := s.localClaims[claim.ClaimKey]
	if !ok || stored.State != ResumeClaimStateClaimed || stored.Version != claim.Version {
		return false, nil
	}
	stored.ClaimedAt = at
	s.localClaims[claim.ClaimKey] = stored
	return true, nil
}

func (s *ResumeAdmissionService) Release(ctx context.Context, claim ResumeClaim) error {
	if s == nil {
		return nil
	}
	if s.claims != nil {
		_, err := s.claims.ReleaseResumeClaim(ctx, claim.ClaimKey, claim.Version)
		return err
	}
	s.localMu.Lock()
	defer s.localMu.Unlock()
	stored := s.localClaims[claim.ClaimKey]
	if stored.State != ResumeClaimStateClaimed || stored.Version != claim.Version {
		return nil
	}
	stored.State = ResumeClaimStateReleased
	stored.Version++
	s.localClaims[claim.ClaimKey] = stored
	return nil
}

func (s *ResumeAdmissionService) getClaim(ctx context.Context, claimKey string) (*ResumeClaim, error) {
	claimKey = strings.TrimSpace(claimKey)
	if claimKey == "" {
		return nil, nil
	}
	if s != nil && s.claims != nil {
		claim, err := s.claims.GetResumeClaim(ctx, claimKey)
		if err != nil {
			return nil, err
		}
		return claim, nil
	}
	if s == nil {
		return nil, nil
	}
	s.localMu.Lock()
	defer s.localMu.Unlock()
	claim, ok := s.localClaims[claimKey]
	if !ok {
		return nil, nil
	}
	return &claim, nil
}

func (s *ResumeAdmissionService) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// DeriveResumeClaimKey is the stable exactly-once key for one settlement
// cohort. Several wake sources (critical lifecycle + settlement sweep) for the
// same parked epoch share the cohort and therefore the same claim. A later park
// epoch in the same episode has a new cohort and may resume again. Without a
// cohort the episode itself is the fallback, which is the conservative
// one-resume-per-episode behavior for legacy records.
func DeriveResumeClaimKey(sessionID, turnID, episodeID, cohortID, settlementID string) string {
	sessionID = strings.TrimSpace(sessionID)
	turnID = strings.TrimSpace(turnID)
	episodeID = strings.TrimSpace(episodeID)
	cohortID = strings.TrimSpace(cohortID)
	settlementID = strings.TrimSpace(settlementID)
	if sessionID == "" || turnID == "" || episodeID == "" {
		return ""
	}
	parts := []string{sessionID, turnID, episodeID}
	if cohortID != "" {
		parts = append(parts, cohortID)
	} else if settlementID != "" {
		parts = append(parts, settlementID)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "rc-" + hex.EncodeToString(sum[:16])
}

func normalizeResumeAdmissionInput(in ResumeAdmissionInput) ResumeAdmissionInput {
	in.RootScopeID = strings.TrimSpace(in.RootScopeID)
	in.ParentSessionID = strings.TrimSpace(in.ParentSessionID)
	in.ParentTurnID = strings.TrimSpace(in.ParentTurnID)
	in.ParentEpisodeID = strings.TrimSpace(in.ParentEpisodeID)
	in.ObligationID = strings.TrimSpace(in.ObligationID)
	in.SettlementID = strings.TrimSpace(in.SettlementID)
	in.SettlementCohortID = strings.TrimSpace(in.SettlementCohortID)
	in.FinalizationPolicy = normalizeAdmissionPolicy(in.FinalizationPolicy)
	in.WakeID = strings.TrimSpace(in.WakeID)
	in.EventKind = strings.TrimSpace(in.EventKind)
	in.WakeReason = strings.TrimSpace(in.WakeReason)
	return in
}

func normalizeAdmissionPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case string(subagentbatch.FinalizationPolicyDetached):
		return string(subagentbatch.FinalizationPolicyDetached)
	case string(subagentbatch.FinalizationPolicyExplicitFollowup), "explicit-followup":
		return string(subagentbatch.FinalizationPolicyExplicitFollowup)
	case "":
		return ""
	default:
		return string(subagentbatch.FinalizationPolicyRequired)
	}
}

func normalizeParentTurnState(state ParentTurnState) ParentTurnState {
	switch ParentTurnState(strings.ToLower(strings.TrimSpace(string(state)))) {
	case ParentTurnRunning:
		return ParentTurnRunning
	case ParentTurnParked:
		return ParentTurnParked
	case ParentTurnFinalized:
		return ParentTurnFinalized
	case ParentTurnCanceled:
		return ParentTurnCanceled
	default:
		return ParentTurnUnknown
	}
}

func isBusyRuntimeStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "waiting_approval", "waiting_input", "rewinding":
		return true
	default:
		return false
	}
}

func isTerminalAdmissionWake(in ResumeAdmissionInput) bool {
	if strings.EqualFold(strings.TrimSpace(in.EventKind), WakeEventTerminal) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(in.WakeReason), WakeReasonObligationSettled) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(in.EventKind), WakeEventLifecycle) &&
		strings.TrimSpace(in.ObligationID) != "" {
		return true
	}
	return false
}
