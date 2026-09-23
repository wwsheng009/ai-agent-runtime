package supervision

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewResumePolicyGate_NeverDefers pins the A6 static-gate contract: a
// depth / visibility restriction must surface as Allowed+Restricted, never as a
// deferral — waiting cannot satisfy a static policy, so queueing on it would
// only reproduce G12's "永久排队变相死锁".
func TestNewResumePolicyGate_NeverDefers(t *testing.T) {
	ctx := context.Background()
	req := ResumeCapacityRequest{TargetParentSessionID: "root-session-gate"}

	require.Nil(t, NewResumePolicyGate(nil), "nil view ⇒ nil 探测（未接线 = 旧行为）")

	allow := NewResumePolicyGate(func(context.Context, ResumeCapacityRequest) ResumePolicyView {
		return ResumePolicyView{}
	})
	verdict, err := allow.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed)
	require.False(t, verdict.Restricted)

	restrict := NewResumePolicyGate(func(context.Context, ResumeCapacityRequest) ResumePolicyView {
		return ResumePolicyView{Restricted: true, Reason: ResumeGateDepth, Detail: "depth=1 max=1"}
	})
	verdict, err = restrict.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "静态策略不得转成 defer（否则纯汇报型 resume 永久排队）")
	require.True(t, verdict.Restricted)
	require.Equal(t, ResumeGateDepth, verdict.Reason)
	require.Equal(t, "depth=1 max=1", verdict.Detail)
}

// TestCombineResumeProbes_Precedence pins the A6 merge order: defer 优先于
// restrict（容量可等到，wake 必须留在 FIFO），restrict 优先于放行，探测报错
// fail-open。
func TestCombineResumeProbes_Precedence(t *testing.T) {
	ctx := context.Background()
	req := ResumeCapacityRequest{TargetParentSessionID: "root-session-gate"}

	require.Nil(t, CombineResumeProbes(nil, nil), "全 nil ⇒ 未接线，旧行为")

	deferred := ResumeCapacityProbeFunc(func(context.Context, ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
		return ResumeCapacityVerdict{Reason: ResumeGateConcurrency, Detail: "subagent slots exhausted"}, nil
	})
	restricted := NewResumePolicyGate(func(context.Context, ResumeCapacityRequest) ResumePolicyView {
		return ResumePolicyView{Restricted: true, Reason: ResumeGateVisibility, Detail: "read_only"}
	})
	failing := ResumeCapacityProbeFunc(func(context.Context, ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
		return ResumeCapacityVerdict{}, errors.New("limiter unreadable")
	})
	allow := ResumeCapacityProbeFunc(func(context.Context, ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
		return ResumeCapacityVerdict{Allowed: true}, nil
	})

	verdict, err := CombineResumeProbes(restricted, deferred).CanResume(ctx, req)
	require.NoError(t, err)
	require.False(t, verdict.Allowed, "defer 优先于 restrict：容量可以等到")
	require.Equal(t, ResumeGateConcurrency, verdict.Reason)

	verdict, err = CombineResumeProbes(failing, allow, restricted).CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed)
	require.True(t, verdict.Restricted)
	require.Equal(t, ResumeGateVisibility, verdict.Reason)
	require.Equal(t, "read_only", verdict.Detail)

	verdict, err = CombineResumeProbes(allow, failing).CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed)
	require.False(t, verdict.Restricted)
}

// TestDigest_SetResumeGate_RendersNotice pins the parent-facing surface: the
// notice is part of the rendered digest Text, and an unwired digest stays
// byte-identical (no resume_gate line at all).
func TestDigest_SetResumeGate_RendersNotice(t *testing.T) {
	require.NotContains(t, formatDigestText(&Digest{}), "resume_gate", "未接线宿主字节级不变")

	digest := &Digest{}
	digest.SetResumeGate(ResumeGateDepth, "depth=1 max=1")
	require.Equal(t, ResumeGateDepth, digest.ResumeGate.Reason)
	require.Contains(t, digest.Text, "resume_gate: depth（depth=1 max=1）")
	require.Contains(t, digest.Text, "本回合不得再派发子任务")

	// 空 reason 兜底到 visibility 词表，避免出现裸 "resume_gate: " 行。
	fallback := &Digest{}
	fallback.SetResumeGate("", "")
	require.Contains(t, fallback.Text, "resume_gate: "+ResumeGateVisibility)
}

// TestWakeConsumer_RestrictedResumeDeliversWithNotice is the A6 core for the
// static gate: the resume must still start a turn, must consume the wake, and
// must carry the restriction inside the delivered digest.
func TestWakeConsumer_RestrictedResumeDeliversWithNotice(t *testing.T) {
	store := newTestStore(t, "resume-gate-restricted")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, unlimitedWakeConfig())

	var got *Digest
	delivered := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		ResumeCapacity: NewResumePolicyGate(func(_ context.Context, req ResumeCapacityRequest) ResumePolicyView {
			require.Equal(t, "root-session-gate", req.RootScopeID)
			require.Len(t, req.WakeIDs, 1)
			return ResumePolicyView{Restricted: true, Reason: ResumeGateDepth, Detail: "depth=1 max=1"}
		}),
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			delivered++
			got = digest
			return nil
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-gate",
		TargetParentSessionID: "root-session-gate",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-gate",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-gate", "", "root-session-gate"))
	require.Equal(t, 1, delivered, "静态限制不得拦住 resume：纯汇报型回合仍必须开")
	require.NotNil(t, got)
	require.NotNil(t, got.ResumeGate)
	require.Equal(t, ResumeGateDepth, got.ResumeGate.Reason)
	require.Contains(t, got.Text, "resume_gate: depth")
	require.Contains(t, got.Text, "本回合不得再派发子任务")

	queued, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-gate", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, queued, "restricted 是放行而不是排队：wake 必须被消费")
}
