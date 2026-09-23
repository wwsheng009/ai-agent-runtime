package supervision

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// staticObligationSource stands in for the durable batch-ledger projection so
// the resume contract can be pinned without a real batch store.
type staticObligationSource struct {
	obligations []ObligationRef
	err         error
	parents     []string
}

func (s *staticObligationSource) ListObligations(_ context.Context, parentSessionID string) ([]ObligationRef, error) {
	s.parents = append(s.parents, parentSessionID)
	if s.err != nil {
		return nil, s.err
	}
	return s.obligations, nil
}

// terminalObligationLedger is the §16.4 "全终态 rollup" fixture: one clean
// completion and one partial completion with a retryable failure, both
// belonging to turn-1.
func terminalObligationLedger() []ObligationRef {
	now := time.Now().UTC()
	return []ObligationRef{
		{
			ID: "batch-1", Kind: "batch", ParentTurnID: "turn-1",
			State: ObligationStateCompleted, Terminal: true,
			Total: 2, Completed: 2, LastProgressAt: now,
			ResultSummary: "both children reported clean results",
			ArtifactRefs:  []string{"art-batch-1"},
		},
		{
			ID: "batch-2", Kind: "batch", ParentTurnID: "turn-1",
			State: ObligationStateCompletedWithFailures, Terminal: true,
			Total: 3, Completed: 2, Failed: 1, LastProgressAt: now,
			Failures: []FailureItem{{
				TaskID:     "task-2",
				State:      ObligationStateFailed,
				ErrorClass: "tool_error",
				ErrorCode:  "exit_status",
				Retryable:  true,
				ResultRef:  "art-task-2",
				Summary:    "unit tests failed",
			}},
			ArtifactRefs: []string{"art-task-2"},
		},
	}
}

// TestResumeContext_TerminalLedgerRollsUpAndReanchorsTurn pins AC-P1-1d and
// §16.4: a fully terminal ledger must produce can_finalize=true plus the
// failure receipt the final report needs, without the model replaying history
// (H4).
func TestResumeContext_TerminalLedgerRollsUpAndReanchorsTurn(t *testing.T) {
	source := &staticObligationSource{obligations: terminalObligationLedger()}
	rc := BuildResumeContext(context.Background(), source, staticProgressSource{groups: []ProgressGroup{runningProgressGroup()}}, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		RootScopeID:     "root-session-1",
		TurnID:          "turn-1",
	})

	require.Equal(t, []string{"root-session-1"}, source.parents, "the projection reads the parent session's ledger")
	require.Equal(t, 2, rc.TotalCount)
	require.Equal(t, 0, rc.PendingCount)
	require.True(t, rc.Terminal, "all obligations terminal ⇒ the turn may finalize")
	require.Equal(t, ObligationStateCompletedWithFailures, rc.Status)
	require.False(t, rc.Truncated)
	require.Empty(t, rc.SourceError)

	require.Len(t, rc.Failures, 1, "the failed item must reach the final report")
	require.Equal(t, "task-2", rc.Failures[0].TaskID)
	require.Equal(t, "tool_error", rc.Failures[0].ErrorClass)
	require.True(t, rc.Failures[0].Retryable, "H3: the receipt carries the retry verdict")
	require.Equal(t, "art-task-2", rc.Failures[0].ResultRef)
	require.Equal(t, "batch-2", rc.Failures[0].BatchID, "flattened failures stay attributable to their batch")
	require.ElementsMatch(t, []string{"art-batch-1", "art-task-2"}, rc.ArtifactRefs)

	// The parked turn keeps its identity when the ledger agrees (I3).
	require.Equal(t, "turn-1", rc.TurnID)

	for _, want := range []string{
		"[Child lifecycle resume]",
		"turn_id: turn-1",
		"obligations: total=2 pending=0 can_finalize=true",
		"rollup_status: completed_with_failures",
		"progress:",
		"terminal rollup:",
		"batch batch-2: status=completed_with_failures total=3 completed=2 failed=1",
		"failed items:",
		"state=failed error_class=tool_error error_code=exit_status retryable=true result_ref=art-task-2",
		"artifact_refs: art-batch-1, art-task-2",
	} {
		require.Contains(t, rc.Text, want, "resume text must stay parseable and complete")
	}

	prompt := ResumePrompt(rc)
	require.Contains(t, prompt, "resume：子任务生命周期事件触发**同一 turn 续跑**")
	require.Contains(t, prompt, "turn_id=turn-1")
	require.Contains(t, prompt, "所有 obligation 均已终态：请直接产出终局报告")
	require.Contains(t, prompt, "can_finalize=true")

	// The consumer must hand exactly this text to the resuming turn.
	require.Equal(t, prompt, AutoWakePromptFor(rc))
	require.True(t, strings.HasSuffix(prompt, "\n"))
}

// TestResumeContext_PendingLedgerBlocksFinalize pins I1: as long as one
// obligation is non-terminal the parent turn must not be allowed to close.
func TestResumeContext_PendingLedgerBlocksFinalize(t *testing.T) {
	source := &staticObligationSource{obligations: []ObligationRef{
		{ID: "batch-1", Kind: "batch", ParentTurnID: "turn-1", State: ObligationStateCompleted, Terminal: true, Total: 2, Completed: 2},
		{ID: "batch-2", Kind: "batch", ParentTurnID: "turn-1", State: ObligationStateRunning, Total: 3, Completed: 1, Failed: 1},
	}}

	rc := BuildResumeContext(context.Background(), source, nil, ResumeContextRequest{ParentSessionID: "root-session-1"})

	require.Equal(t, 1, rc.PendingCount)
	require.False(t, rc.Terminal, "I1: a running obligation keeps the join open")
	require.Equal(t, ObligationStateRunning, rc.Status)
	require.Contains(t, rc.Text, "obligations: total=2 pending=1 can_finalize=false")
	require.Contains(t, rc.Text, "batch batch-2: status=running total=3 completed=1 failed=1 (not terminal)")

	prompt := AutoWakePromptFor(rc)
	require.Contains(t, prompt, "仍有未终态 obligation：不得收尾（I1）")
	require.NotContains(t, prompt, "请直接产出终局报告")
}

// TestResumeContext_TurnAnchorPrecedence pins the I3 anchoring rules: a live
// obligation's turn wins over the scheduling hint, the hint wins when the
// ledger only holds finished rows from another turn, and the ledger is the
// last resort (the host then falls back to a new turn).
func TestResumeContext_TurnAnchorPrecedence(t *testing.T) {
	ctx := context.Background()

	pending := []ObligationRef{{ID: "batch-1", State: ObligationStateRunning, ParentTurnID: "turn-1"}}
	terminal := []ObligationRef{{ID: "batch-0", State: ObligationStateCompleted, Terminal: true, ParentTurnID: "turn-0"}}

	rc := BuildResumeContext(ctx, &staticObligationSource{obligations: pending}, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		TurnID:          "turn-stale",
	})
	require.Equal(t, "turn-1", rc.TurnID, "a live obligation's turn is authoritative")

	rc = BuildResumeContext(ctx, &staticObligationSource{obligations: terminal}, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		TurnID:          "turn-2",
	})
	require.Equal(t, "turn-2", rc.TurnID, "a finished ledger must not pull the resume into an ended turn")

	rc = BuildResumeContext(ctx, &staticObligationSource{obligations: terminal}, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
	})
	require.Equal(t, "turn-0", rc.TurnID, "the ledger backfills a lost hint")
}

// TestResumeContext_AggregateStatusSeverity pins the §16.4 aggregate
// enunciation: the worst terminal family of the turn wins, so a canceled or
// failed item is never reported as a clean completion. Rows are attributed to
// one turn because only turn-attributed rows gate the join (EC-G4).
func TestResumeContext_AggregateStatusSeverity(t *testing.T) {
	ctx := context.Background()
	completed := ObligationRef{ID: "batch-ok", Kind: "batch", ParentTurnID: "turn-1", State: ObligationStateCompleted, Terminal: true, Total: 1, Completed: 1}
	build := func(rows ...ObligationRef) *ResumeContext {
		return BuildResumeContext(ctx, &staticObligationSource{obligations: rows}, nil, ResumeContextRequest{ParentSessionID: "root-session-1"})
	}

	cases := []struct {
		name string
		rows []ObligationRef
		want string
	}{
		{name: "all completed", rows: []ObligationRef{completed}, want: ObligationStateCompleted},
		{
			name: "partial completion",
			rows: []ObligationRef{completed, {ID: "batch-partial", State: ObligationStateCompletedWithFailures, Terminal: true, Total: 2, Completed: 1, Failed: 1}},
			want: ObligationStateCompletedWithFailures,
		},
		{
			name: "a canceled item outranks a partial completion",
			rows: []ObligationRef{completed, {ID: "batch-canceled", State: ObligationStateCanceled, Terminal: true, Total: 1, Skipped: 1}},
			want: ObligationStateCanceled,
		},
		{
			name: "a timed out item outranks a canceled one",
			rows: []ObligationRef{completed, {ID: "batch-timedout", State: ObligationStateTimedOut, Terminal: true, Total: 1, Failed: 1}},
			want: ObligationStateTimedOut,
		},
		{
			name: "a failed item is the worst terminal family",
			rows: []ObligationRef{completed, {ID: "batch-failed", State: ObligationStateFailed, Terminal: true, Total: 1, Failed: 1}},
			want: ObligationStateFailed,
		},
		{
			name: "live rows keep the join open",
			rows: []ObligationRef{completed, {ID: "batch-running", ParentTurnID: "turn-1", State: ObligationStateRunning, Total: 3, Completed: 1}},
			want: ObligationStateRunning,
		},
		{
			name: "only queued rows are pending",
			rows: []ObligationRef{{ID: "batch-queued", ParentTurnID: "turn-1", State: "queued", Total: 1}},
			want: ObligationStatePending,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := build(tc.rows...)
			require.Equal(t, tc.want, rc.Status)
			for _, ob := range rc.Obligations {
				require.Equal(t, obligationTerminal(ob.State), ob.Terminal, "terminal flags must be normalized in the projection")
			}
		})
	}

	// A canceled terminal row renders its skipped count instead of a failure
	// count, so the parent does not read cancellation as lost work.
	rc := build(completed, ObligationRef{ID: "batch-canceled", Kind: "batch", State: ObligationStateCanceled, Terminal: true, Total: 1, Skipped: 1})
	require.Contains(t, rc.Text, "batch batch-canceled: status=canceled total=1 completed=0 failed=0 skipped=1")
	require.Empty(t, rc.Failures, "a canceled item is not a failure item")
}

// TestResumeContext_BudgetBoundsLedgerContent pins AC-P1-1d: the resume block
// is bounded by items AND characters, and a cut announces itself instead of
// silently dropping the receipt.
func TestResumeContext_BudgetBoundsLedgerContent(t *testing.T) {
	obligations := make([]ObligationRef, 0, 40)
	for i := 0; i < 40; i++ {
		ob := ObligationRef{
			ID:           "batch-" + strings.Repeat("x", 8) + string(rune('a'+i%26)) + strings.Repeat("y", i%7),
			Kind:         "batch",
			ParentTurnID: "turn-1",
			State:        ObligationStateCompletedWithFailures,
			Terminal:     true,
			Total:        3, Completed: 2, Failed: 1,
			ResultSummary: strings.Repeat("bounded preview ", 40),
		}
		for j := 0; j < 3; j++ {
			ob.Failures = append(ob.Failures, FailureItem{
				TaskID:     "task-" + strings.Repeat("z", 12),
				State:      ObligationStateFailed,
				ErrorClass: "tool_error",
				Retryable:  true,
				Summary:    strings.Repeat("failure detail ", 30),
			})
		}
		obligations = append(obligations, ob)
	}

	rc := BuildResumeContext(context.Background(), &staticObligationSource{obligations: obligations}, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
	})
	require.True(t, rc.Truncated, "the projection must report that the budget cut content")
	require.LessOrEqual(t, len(rc.Obligations), DefaultConfig().DigestMaxItems)
	require.LessOrEqual(t, len(rc.Failures), DefaultConfig().DigestMaxItems)
	require.LessOrEqual(t, len([]rune(rc.Text)), DefaultConfig().DigestMaxChars)
	require.Contains(t, rc.Text, "[truncated: resume context exceeds budget", "a character-budget cut must announce itself")

	// A ledger cut by the item budget alone still renders the item marker (the
	// text stays short enough that the character budget does not interfere).
	light := make([]ObligationRef, 0, 40)
	for i := 0; i < 40; i++ {
		light = append(light, ObligationRef{
			ID: "batch-light-" + string(rune('a'+i%26)), Kind: "batch",
			ParentTurnID: "turn-1", State: ObligationStateCompleted, Terminal: true, Total: 1, Completed: 1,
		})
	}
	lightRC := BuildResumeContext(context.Background(), &staticObligationSource{obligations: light}, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
	})
	require.True(t, lightRC.Truncated)
	require.Len(t, lightRC.Obligations, DefaultConfig().DigestMaxItems)
	require.Contains(t, lightRC.Text, "truncated: true (budget reached")

	// A tight explicit budget is honored on top of the item caps.
	tight := BuildResumeContext(context.Background(), &staticObligationSource{obligations: obligations}, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		Budget:          ResumeBudget{MaxItems: 2, MaxChars: 200},
	})
	require.Len(t, tight.Obligations, 2)
	require.LessOrEqual(t, len([]rune(tight.Text)), 200, "the character budget must be enforced on a rune boundary")
	require.Contains(t, tight.Text, "[truncated: resume context exceeds budget")
}

// TestResumeContext_LedgerErrorDegrades pins the best-effort contract: a
// broken ledger read yields an "unknown" verdict, never a dropped wake or a
// falsely finalizable turn.
func TestResumeContext_LedgerErrorDegrades(t *testing.T) {
	source := &staticObligationSource{err: errors.New("ledger unreachable")}
	rc := BuildResumeContext(context.Background(), source, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		TurnID:          "turn-1",
	})

	require.Equal(t, -1, rc.PendingCount)
	require.False(t, rc.Terminal)
	require.Equal(t, "unknown", rc.Status)
	require.Equal(t, "ledger unreachable", rc.SourceError)
	require.Contains(t, rc.Text, "obligations: unknown (no ledger projection wired); can_finalize=unknown")
	require.Contains(t, rc.Text, "ledger_projection_error: ledger unreachable")

	prompt := AutoWakePromptFor(rc)
	require.Contains(t, prompt, "resume：子任务生命周期事件触发**同一 turn 续跑**")
	require.Contains(t, prompt, "turn_id=turn-1")
	require.Contains(t, prompt, "can_finalize=unknown")
	require.NotContains(t, prompt, "请直接产出终局报告", "an unknown verdict must not authorize finalizing")

	// A projection error must not be reported as a progress verdict either.
	require.Empty(t, rc.Rollup)
}

// TestResumeContext_UNwiredProjectionsStayLegacy pins the unwired-host
// behavior: no ledger ⇒ the wake keeps the byte-identical legacy prompt.
func TestResumeContext_UNwiredProjectionsStayLegacy(t *testing.T) {
	rc := BuildResumeContext(context.Background(), nil, nil, ResumeContextRequest{})
	require.NotNil(t, rc)
	require.Equal(t, -1, rc.PendingCount)
	require.Equal(t, 0, rc.TotalCount)
	require.Equal(t, "unknown", rc.Status)
	require.Contains(t, rc.Text, "[Child lifecycle resume]")

	require.Equal(t, AutoWakePrompt, AutoWakePromptFor(nil))
	require.Equal(t, AutoWakePrompt, AutoWakePromptFor(&ResumeContext{}), "an empty resume text keeps the legacy prompt")
	require.Contains(t, AutoWakePromptFor(rc), "resume：")
}

// TestResumeContext_NilSourceIsSafe guards the defensive nil-receiver paths the
// hosts rely on during early wiring (a nil scheduler must not panic).
func TestResumeContext_NilSourceIsSafe(t *testing.T) {
	require.Nil(t, NewBatchObligationSource(nil), "an unwired batch store keeps the legacy wake path")

	var scheduler *WakeScheduler
	scheduler.SetObligationSource(nil)
	require.Nil(t, scheduler.BuildResumeContext(context.Background(), ResumeContextRequest{ParentSessionID: "s"}))

	var source *BatchObligationSource
	obligations, err := source.ListObligations(context.Background(), "root-session-1")
	require.NoError(t, err)
	require.Empty(t, obligations)
}

// TestWakeConsumer_DeliversResumeContext is the L1 closure of AC-P1-1a/1d: the
// delivery path prefers the resume payload, and the prompt handed to the parked
// turn carries the rollup under the same turn id.
func TestWakeConsumer_DeliversResumeContext(t *testing.T) {
	store := newTestStore(t, "wake-consumer-resume")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		Obligations: &staticObligationSource{obligations: []ObligationRef{{
			ID: "batch-1", Kind: "batch", ParentTurnID: "turn-1",
			State: ObligationStateRunning, Total: 2, Completed: 1,
		}}},
		TurnHint: func(context.Context, string) string { return "turn-hint" },
	})
	var mu sync.Mutex
	var resumes []*ResumeContext
	legacy := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(context.Context, string, string, *Digest, []string) error {
			legacy++
			return nil
		},
		DeliverResume: func(_ context.Context, _, _ string, digest *Digest, wakeIDs []string, resume *ResumeContext) error {
			mu.Lock()
			defer mu.Unlock()
			require.NotNil(t, digest)
			require.Len(t, wakeIDs, 1)
			resumes = append(resumes, resume)
			return nil
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
		TurnID:                "turn-hint",
	})
	require.NoError(t, err)

	// C2-1：通知携带 turn_id，wake 行落库时可被 resume 复用。
	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "turn-hint", pending[0].TurnID)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 0, legacy, "a wired resume path must not double-submit the legacy turn")
	require.Len(t, resumes, 1)
	resume := resumes[0]
	require.NotNil(t, resume)
	require.Equal(t, "turn-1", resume.TurnID, "the live obligation's turn wins over the scheduling hint")
	require.Equal(t, 1, resume.PendingCount)
	require.False(t, resume.Terminal)

	prompt := AutoWakePromptFor(resume)
	require.Contains(t, prompt, "resume：")
	require.Contains(t, prompt, "turn_id=turn-1")
	require.Contains(t, prompt, "can_finalize=false")
	require.Contains(t, prompt, "batch batch-1: status=running total=2 completed=1 failed=0 (not terminal)")
}

// TestWakeConsumer_ResumeBuilderPanicKeepsWakeDurable verifies the panic
// containment around the projection: a buggy builder must not swallow the
// parent's only auto-wake.
func TestWakeConsumer_ResumeBuilderPanicKeepsWakeDurable(t *testing.T) {
	store := newTestStore(t, "wake-consumer-resume-panic")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	delivered := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
		ResumeBuilder: func(context.Context, ResumeContextRequest) *ResumeContext {
			panic("ledger projection exploded")
		},
		DeliverResume: func(_ context.Context, _, _ string, _ *Digest, _ []string, resume *ResumeContext) error {
			require.Nil(t, resume, "a panicking builder degrades to the legacy digest prompt")
			delivered++
			return nil
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.Equal(t, 1, delivered)
}
