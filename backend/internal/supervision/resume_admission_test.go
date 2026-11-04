package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

type staticParentTurnResolver struct {
	snapshot *ParentTurnSnapshot
	err      error
}

func (r staticParentTurnResolver) ResolveParentTurn(context.Context, string) (*ParentTurnSnapshot, error) {
	return r.snapshot, r.err
}

type stubAdmissionBackgroundJobs map[string]bool

func (s stubAdmissionBackgroundJobs) BackgroundJobTerminal(_ context.Context, jobID string) (bool, bool, error) {
	terminal, found := s[jobID]
	return terminal, found, nil
}

func newAdmissionBatchStore(t *testing.T, name string) subagentbatch.BatchStore {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedAdmissionParkedBatch(t *testing.T, store subagentbatch.BatchStore, batchStatus subagentbatch.BatchStatus, episodeID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	batchID := "batch-admit"
	created, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     "sess-admit",
		ParentSessionID: "sess-admit",
		ParentTurnID:    "turn-admit",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          batchStatus,
		TaskCount:       1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, store.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-admit",
		EpisodeID:     episodeID,
		SessionID:     "sess-admit",
		RootScopeID:   "sess-admit",
		ObligationIDs: []string{batchID},
		ParkedAt:      now,
	}))
	return batchID
}

func admissionParkedSnapshot(sessionID, turnID, episodeID string) *ParentTurnSnapshot {
	return &ParentTurnSnapshot{
		SessionID:          sessionID,
		Status:             "idle",
		SuspendedTurnID:    turnID,
		SuspendedEpisodeID: episodeID,
		Phase:              ParentTurnParked,
	}
}

func TestResumeAdmissionMatrix(t *testing.T) {
	ctx := context.Background()
	baseInput := ResumeAdmissionInput{
		RootScopeID:        "sess-admit",
		ParentSessionID:    "sess-admit",
		ParentTurnID:       "turn-admit",
		ParentEpisodeID:    "ep-admit",
		FinalizationPolicy: string(subagentbatch.FinalizationPolicyRequired),
		EventKind:          WakeEventTerminal,
	}

	t.Run("required matching parked episode", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-required")
		batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-admit")
		store := newTestStore(t, "admit-required-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
			Claims:       store,
		})
		in := baseInput
		in.ObligationID = batchID
		result, err := service.Evaluate(ctx, in)
		require.NoError(t, err)
		require.Equal(t, ResumeSameParkedTurn, result.Decision)
		require.NotEmpty(t, result.ClaimKey)
	})

	t.Run("required background job resumes same parked episode", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-required-job")
		require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
			TurnID:        "turn-admit",
			EpisodeID:     "ep-admit",
			SessionID:     "sess-admit",
			RootScopeID:   "sess-admit",
			ObligationIDs: []string{subagentbatch.BackgroundJobObligationID("job-admit")},
			ParkedAt:      time.Now().UTC(),
		}))
		store := newTestStore(t, "admit-required-job-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:    batches,
			ParentStates:   staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
			BackgroundJobs: stubAdmissionBackgroundJobs{"job-admit": true},
			Claims:         store,
		})
		in := baseInput
		in.ObligationID = "job-admit"
		result, err := service.Evaluate(ctx, in)
		require.NoError(t, err)
		require.Equal(t, ResumeSameParkedTurn, result.Decision)
		require.NotEmpty(t, result.ClaimKey)
	})

	t.Run("running required background job retries", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-required-job-running")
		require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
			TurnID:        "turn-admit",
			EpisodeID:     "ep-admit",
			SessionID:     "sess-admit",
			RootScopeID:   "sess-admit",
			ObligationIDs: []string{subagentbatch.BackgroundJobObligationID("job-admit")},
			ParkedAt:      time.Now().UTC(),
		}))
		store := newTestStore(t, "admit-required-job-running-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:    batches,
			ParentStates:   staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
			BackgroundJobs: stubAdmissionBackgroundJobs{"job-admit": false},
			Claims:         store,
		})
		in := baseInput
		in.ObligationID = "job-admit"
		result, err := service.Evaluate(ctx, in)
		require.NoError(t, err)
		require.Equal(t, RetryAdmission, result.Decision)
		require.Equal(t, "required_obligations_pending", result.Reason)
	})

	t.Run("required pending obligation retries", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-pending")
		batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchRunning, "ep-admit")
		store := newTestStore(t, "admit-pending-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
			Claims:       store,
		})
		in := baseInput
		in.ObligationID = batchID
		result, err := service.Evaluate(ctx, in)
		require.NoError(t, err)
		require.Equal(t, RetryAdmission, result.Decision)
		require.Equal(t, "required_obligations_pending", result.Reason)
	})

	t.Run("finalized parent is notification only", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-finalized")
		batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-admit")
		store := newTestStore(t, "admit-finalized-supervision")
		snapshot := admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")
		snapshot.Phase = ParentTurnFinalized
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: snapshot},
			Claims:       store,
		})
		in := baseInput
		in.ObligationID = batchID
		result, err := service.Evaluate(ctx, in)
		require.NoError(t, err)
		require.Equal(t, NotifyOnly, result.Decision)
	})

	t.Run("no matching suspension is notification only", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-no-suspension")
		store := newTestStore(t, "admit-no-suspension-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: &ParentTurnSnapshot{SessionID: "sess-admit", Status: "idle", Phase: ParentTurnFinalized}},
			Claims:       store,
		})
		result, err := service.Evaluate(ctx, baseInput)
		require.NoError(t, err)
		require.Equal(t, NotifyOnly, result.Decision)
	})

	t.Run("detached policy is notification only", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-detached")
		seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-admit")
		store := newTestStore(t, "admit-detached-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
			Claims:       store,
		})
		in := baseInput
		in.FinalizationPolicy = string(subagentbatch.FinalizationPolicyDetached)
		result, err := service.Evaluate(ctx, in)
		require.NoError(t, err)
		require.Equal(t, NotifyOnly, result.Decision)
	})

	t.Run("stale episode is rejected", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-stale")
		batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-new")
		store := newTestStore(t, "admit-stale-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-new")},
			Claims:       store,
		})
		in := baseInput
		in.ObligationID = batchID
		result, err := service.Evaluate(ctx, in)
		require.NoError(t, err)
		require.Equal(t, RejectStale, result.Decision)
		require.Equal(t, "episode_mismatch", result.Reason)
	})

	t.Run("turn without episode is legacy and notification only", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-turn-only")
		batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-admit")
		store := newTestStore(t, "admit-turn-only-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
			Claims:       store,
		})
		in := baseInput
		in.ObligationID = batchID
		in.ParentEpisodeID = ""
		result, err := service.Evaluate(ctx, in)
		require.NoError(t, err)
		require.Equal(t, NotifyOnly, result.Decision)
		require.Equal(t, "missing_parent_episode_identity", result.Reason)
	})

	t.Run("missing identity is notification only", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-missing-id")
		store := newTestStore(t, "admit-missing-id-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: &ParentTurnSnapshot{SessionID: "sess-admit", Status: "idle", Phase: ParentTurnFinalized}},
			Claims:       store,
		})
		result, err := service.Evaluate(ctx, ResumeAdmissionInput{
			RootScopeID:     "sess-admit",
			ParentSessionID: "sess-admit",
			EventKind:       WakeEventTerminal,
		})
		require.NoError(t, err)
		require.Equal(t, NotifyOnly, result.Decision)
	})

	t.Run("legacy progress is allowed for idle non-finalized parent", func(t *testing.T) {
		store := newTestStore(t, "admit-legacy-progress")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			ParentStates: staticParentTurnResolver{snapshot: &ParentTurnSnapshot{
				SessionID: "sess-admit",
				Status:    "idle",
				Phase:     ParentTurnUnknown,
			}},
			Claims: store,
		})
		result, err := service.Evaluate(ctx, ResumeAdmissionInput{
			RootScopeID:     "sess-admit",
			ParentSessionID: "sess-admit",
			EventKind:       WakeEventProgress,
			WakeReason:      WakeReasonProgressCheck,
		})
		require.NoError(t, err)
		require.Equal(t, ResumeLegacy, result.Decision)
	})

	t.Run("legacy progress is notification only after finalization", func(t *testing.T) {
		store := newTestStore(t, "admit-legacy-progress-final")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			ParentStates: staticParentTurnResolver{snapshot: &ParentTurnSnapshot{
				SessionID: "sess-admit",
				Status:    "idle",
				Phase:     ParentTurnFinalized,
			}},
			Claims: store,
		})
		result, err := service.Evaluate(ctx, ResumeAdmissionInput{
			RootScopeID:     "sess-admit",
			ParentSessionID: "sess-admit",
			EventKind:       WakeEventProgress,
			WakeReason:      WakeReasonProgressCheck,
		})
		require.NoError(t, err)
		require.Equal(t, NotifyOnly, result.Decision)
		require.Equal(t, "parent_turn_finalized", result.Reason)
	})

	t.Run("legacy progress waits while parent is busy", func(t *testing.T) {
		store := newTestStore(t, "admit-legacy-progress-busy")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			ParentStates: staticParentTurnResolver{snapshot: &ParentTurnSnapshot{
				SessionID: "sess-admit",
				Status:    "running",
				Phase:     ParentTurnRunning,
			}},
			Claims: store,
		})
		result, err := service.Evaluate(ctx, ResumeAdmissionInput{
			RootScopeID:     "sess-admit",
			ParentSessionID: "sess-admit",
			EventKind:       WakeEventProgress,
			WakeReason:      WakeReasonProgressCheck,
		})
		require.NoError(t, err)
		require.Equal(t, RetryAdmission, result.Decision)
	})

	t.Run("missing turn is derived from obligation ledger", func(t *testing.T) {
		batches := newAdmissionBatchStore(t, "admit-derive-turn")
		batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-admit")
		store := newTestStore(t, "admit-derive-turn-supervision")
		service := NewResumeAdmissionService(ResumeAdmissionConfig{
			Suspensions:  batches,
			ParentStates: staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
			Claims:       store,
		})
		result, err := service.Evaluate(ctx, ResumeAdmissionInput{
			RootScopeID:        "sess-admit",
			ParentSessionID:    "sess-admit",
			ObligationID:       batchID,
			FinalizationPolicy: string(subagentbatch.FinalizationPolicyRequired),
			EventKind:          WakeEventTerminal,
		})
		require.NoError(t, err)
		require.Equal(t, ResumeSameParkedTurn, result.Decision)
		require.Equal(t, "turn-admit", result.Input.ParentTurnID)
		require.Equal(t, "ep-admit", result.Input.ParentEpisodeID)
	})
}

func TestResumeAdmissionClaimIsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	batches := newAdmissionBatchStore(t, "admit-claim")
	batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-admit")
	store := newTestStore(t, "admit-claim-supervision")
	service := NewResumeAdmissionService(ResumeAdmissionConfig{
		Suspensions:  batches,
		ParentStates: staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
		Claims:       store,
	})
	result, err := service.Evaluate(ctx, ResumeAdmissionInput{
		RootScopeID:        "sess-admit",
		ParentSessionID:    "sess-admit",
		ParentTurnID:       "turn-admit",
		ParentEpisodeID:    "ep-admit",
		ObligationID:       batchID,
		FinalizationPolicy: string(subagentbatch.FinalizationPolicyRequired),
		EventKind:          WakeEventTerminal,
	})
	require.NoError(t, err)
	require.Equal(t, ResumeSameParkedTurn, result.Decision)

	claim, won, err := service.Claim(ctx, result)
	require.NoError(t, err)
	require.True(t, won)
	require.Equal(t, ResumeClaimStateClaimed, claim.State)

	_, won, err = service.Claim(ctx, result)
	require.NoError(t, err)
	require.False(t, won, "the second claim must lose the durable exactly-once boundary")

	require.NoError(t, service.MarkSubmitted(ctx, claim, "turn-model-1"))
	retry, err := service.Evaluate(ctx, ResumeAdmissionInput{
		RootScopeID:        "sess-admit",
		ParentSessionID:    "sess-admit",
		ParentTurnID:       "turn-admit",
		ParentEpisodeID:    "ep-admit",
		ObligationID:       batchID,
		FinalizationPolicy: string(subagentbatch.FinalizationPolicyRequired),
		EventKind:          WakeEventTerminal,
	})
	require.NoError(t, err)
	require.Equal(t, NotifyOnly, retry.Decision)
	require.Equal(t, "settlement_already_submitted", retry.Reason)
}

func TestResumeAdmissionAllowsNextParkedEpochInSameEpisode(t *testing.T) {
	ctx := context.Background()
	batches := newAdmissionBatchStore(t, "admit-next-epoch")
	batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-admit")
	store := newTestStore(t, "admit-next-epoch-supervision")
	service := NewResumeAdmissionService(ResumeAdmissionConfig{
		Suspensions:  batches,
		ParentStates: staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
		Claims:       store,
	})
	input := ResumeAdmissionInput{
		RootScopeID:        "sess-admit",
		ParentSessionID:    "sess-admit",
		ParentTurnID:       "turn-admit",
		ParentEpisodeID:    "ep-admit",
		ObligationID:       batchID,
		FinalizationPolicy: string(subagentbatch.FinalizationPolicyRequired),
		EventKind:          WakeEventTerminal,
	}
	first, err := service.Evaluate(ctx, input)
	require.NoError(t, err)
	require.Equal(t, ResumeSameParkedTurn, first.Decision)
	claim, won, err := service.Claim(ctx, first)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, service.MarkSubmitted(ctx, claim, "turn-model-1"))

	// Same logical episode, next park epoch: a new dispatch cohort must be able
	// to resume again instead of being blocked by the first settlement claim.
	record, ok, err := batches.GetTurnSuspension(ctx, "sess-admit", "turn-admit")
	require.NoError(t, err)
	require.True(t, ok)
	record.ParkedAt = record.ParkedAt.Add(time.Minute)
	require.NoError(t, batches.ParkTurnSuspension(ctx, record))

	second, err := service.Evaluate(ctx, input)
	require.NoError(t, err)
	require.Equal(t, ResumeSameParkedTurn, second.Decision)
	require.NotEqual(t, first.ClaimKey, second.ClaimKey)
}

func TestResumeAdmissionRecoversStaleClaimAsSubmitted(t *testing.T) {
	ctx := context.Background()
	batches := newAdmissionBatchStore(t, "admit-stale-claim")
	batchID := seedAdmissionParkedBatch(t, batches, subagentbatch.BatchCompleted, "ep-admit")
	store := newTestStore(t, "admit-stale-claim-supervision")
	service := NewResumeAdmissionService(ResumeAdmissionConfig{
		Suspensions:     batches,
		ParentStates:    staticParentTurnResolver{snapshot: admissionParkedSnapshot("sess-admit", "turn-admit", "ep-admit")},
		Claims:          store,
		ClaimStaleAfter: time.Minute,
	})
	input := ResumeAdmissionInput{
		RootScopeID:        "sess-admit",
		ParentSessionID:    "sess-admit",
		ParentTurnID:       "turn-admit",
		ParentEpisodeID:    "ep-admit",
		ObligationID:       batchID,
		FinalizationPolicy: string(subagentbatch.FinalizationPolicyRequired),
		EventKind:          WakeEventTerminal,
	}
	result, err := service.Evaluate(ctx, input)
	require.NoError(t, err)
	require.Equal(t, ResumeSameParkedTurn, result.Decision)
	claim, won, err := service.Claim(ctx, result)
	require.NoError(t, err)
	require.True(t, won)

	db, err := store.dbOrErr()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		UPDATE supervision_resume_claims
		SET claimed_at = ?
		WHERE claim_key = ?
	`, formatSupervisionTime(time.Now().UTC().Add(-10*time.Minute)), claim.ClaimKey)
	require.NoError(t, err)

	recovered, err := service.RecoverStaleClaims(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)

	stored, err := store.GetResumeClaim(ctx, claim.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, ResumeClaimStateSubmitted, stored.State)
	require.Equal(t, "recovered_unknown", stored.ModelTurnID)

	retry, err := service.Evaluate(ctx, input)
	require.NoError(t, err)
	require.Equal(t, NotifyOnly, retry.Decision)
	require.Equal(t, "settlement_already_submitted", retry.Reason)
	require.NotEqual(t, ResumeSameParkedTurn, retry.Decision,
		"crash recovery must not create a second model turn")
}

// TestResumeAdmissionRenewsClaimBeforeRecovery proves the lease heartbeat is a
// real fence: a long-running delivery that refreshes claimed_at must not be
// recovered as stale, and the original Version must remain usable for the
// eventual submitted transition.
func TestResumeAdmissionRenewsClaimBeforeRecovery(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "admit-claim-renew")
	now := time.Now().UTC()
	service := NewResumeAdmissionService(ResumeAdmissionConfig{
		Claims:          store,
		Now:             func() time.Time { return now },
		ClaimStaleAfter: time.Minute,
	})
	claim, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "renew-before-recovery",
		RootScopeID:     "root-renew",
		ParentSessionID: "root-renew",
		ParentTurnID:    "turn-renew",
		ParentEpisodeID: "ep-renew",
		ClaimedAt:       now.Add(-10 * time.Minute),
	})
	require.NoError(t, err)
	require.True(t, won)

	renewed, err := service.RenewClaim(ctx, claim)
	require.NoError(t, err)
	require.True(t, renewed)

	recovered, err := service.RecoverStaleClaims(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered, "a renewed claimed row must stay outside the stale cutoff")
	stored, err := store.GetResumeClaim(ctx, claim.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, ResumeClaimStateClaimed, stored.State)
	require.WithinDuration(t, now, stored.ClaimedAt, time.Millisecond)
	require.Equal(t, claim.Version, stored.Version, "renewal must not invalidate the holder's CAS token")

	require.NoError(t, service.MarkSubmitted(ctx, claim, "turn-renew-model"))
	stored, err = store.GetResumeClaim(ctx, claim.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, ResumeClaimStateSubmitted, stored.State)

	renewed, err = service.RenewClaim(ctx, claim)
	require.NoError(t, err)
	require.False(t, renewed, "a submitted claim can no longer be renewed")
}

func TestResumeAdmissionPrunesTerminalClaimsOnly(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "admit-claim-prune")
	service := NewResumeAdmissionService(ResumeAdmissionConfig{
		Claims:         store,
		ClaimRetention: 24 * time.Hour,
	})
	old := time.Now().UTC().Add(-10 * 24 * time.Hour)
	db, err := store.dbOrErr()
	require.NoError(t, err)

	submitted, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "prune-submitted",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-1",
		ParentEpisodeID: "ep-1",
		ClaimedAt:       old,
	})
	require.NoError(t, err)
	require.True(t, won)
	_, err = store.MarkResumeSubmitted(ctx, submitted.ClaimKey, "model-1", old, submitted.Version)
	require.NoError(t, err)

	released, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "prune-released",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-2",
		ParentEpisodeID: "ep-2",
		ClaimedAt:       old,
	})
	require.NoError(t, err)
	require.True(t, won)
	_, err = store.ReleaseResumeClaim(ctx, released.ClaimKey, released.Version)
	require.NoError(t, err)

	claimed, won, err := store.ClaimResume(ctx, ResumeClaim{
		ClaimKey:        "prune-claimed",
		RootScopeID:     "root-1",
		ParentSessionID: "root-1",
		ParentTurnID:    "turn-3",
		ParentEpisodeID: "ep-3",
		ClaimedAt:       old,
	})
	require.NoError(t, err)
	require.True(t, won)

	_, err = db.ExecContext(ctx, `
		UPDATE supervision_resume_claims
		SET claimed_at = ?, submitted_at = CASE WHEN state = 'submitted' THEN ? ELSE NULL END
		WHERE claim_key IN ('prune-submitted', 'prune-released', 'prune-claimed')
	`, formatSupervisionTime(old), formatSupervisionTime(old))
	require.NoError(t, err)

	pruned, err := service.PruneClaims(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, pruned)

	storedSubmitted, err := store.GetResumeClaim(ctx, submitted.ClaimKey)
	require.NoError(t, err)
	require.Nil(t, storedSubmitted)
	storedReleased, err := store.GetResumeClaim(ctx, released.ClaimKey)
	require.NoError(t, err)
	require.Nil(t, storedReleased)
	storedClaimed, err := store.GetResumeClaim(ctx, claimed.ClaimKey)
	require.NoError(t, err)
	require.NotNil(t, storedClaimed, "claimed rows must never be pruned")
	require.Equal(t, ResumeClaimStateClaimed, storedClaimed.State)
}

func TestDrainRunnableWithAdmissionConsumesNotifyOnlyWithoutTurn(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "admit-drain-notify")
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	_, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "sess-drain",
		TargetParentSessionID: "sess-drain",
		TurnID:                "turn-drain",
		ParentEpisodeID:       "ep-drain",
		FinalizationPolicy:    string(subagentbatch.FinalizationPolicyDetached),
		WakeReason:            "background_job_completed",
		ObligationID:          "job-1",
		EventKind:             WakeEventTerminal,
		EventSeq:              9,
	})
	require.NoError(t, err)

	claimed, digest, err := scheduler.DrainRunnableWithAdmission(ctx, "sess-drain", "", "sess-drain", nil,
		func(context.Context, WakePending) (ResumeAdmissionDecision, error) {
			return NotifyOnly, nil
		})
	require.NoError(t, err)
	require.Empty(t, claimed)
	require.Nil(t, digest)
	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "sess-drain", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "notification-only must close the wake row")
	delivered, err := store.IsWakeDelivered(ctx, DeriveNotifyKey("turn-drain", "job-1", WakeEventTerminal, 9))
	require.NoError(t, err)
	require.True(t, delivered, "notification-only consumption must suppress replay wakes")
}
