package supervision

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// 2026-10-09 真机回归：spawn_agent 轻量子会话是 agent_session: 义务，只存在于
// §6.12 挂起记录里。只按 batch 表投影会让 resume 上下文输出
// "obligations: total=0 pending=0 can_finalize=true"，在子代理仍在运行时宣告
// "所有 obligation 均已终态"（wait_feedback 唤醒 episode）。本组用例钉住修复：
// 挂起记录的 agent_session:/team: 义务必须进入 resume 账本，并且只有与
// settle/wait 完全同源的 resolver 判成终态时才算完成。

// stubAgentSessionObligationResolver maps child session id -> terminal; an
// absent id models "no durable lifecycle row" (still running / never created).
type stubAgentSessionObligationResolver map[string]bool

func (s stubAgentSessionObligationResolver) AgentSessionTerminal(_ context.Context, sessionID string) (bool, bool, error) {
	terminal, found := s[sessionID]
	if !found {
		return false, false, nil
	}
	return terminal, true, nil
}

func parkedChildRecord() *subagentbatch.TurnSuspension {
	return &subagentbatch.TurnSuspension{
		TurnID:      "turn-1",
		SessionID:   "root-session-1",
		RootScopeID: "root-session-1",
		ObligationIDs: []string{
			subagentbatch.AgentSessionObligationID("child-live"),
			subagentbatch.AgentSessionObligationID("child-done"),
			subagentbatch.AgentSessionObligationID("child-missing"),
			subagentbatch.TeamObligationID("team-live"),
			subagentbatch.TeamObligationID("team-done"),
		},
	}
}

// TestBatchObligationSource_ProjectsParkedAgentSessionAndTeamObligations pins
// the fix: the parked record's child-session / team obligations join the resume
// ledger with their durable state, and a live (or unreadable) child keeps the
// join open.
func TestBatchObligationSource_ProjectsParkedAgentSessionAndTeamObligations(t *testing.T) {
	ctx := context.Background()
	store := &fakeBatchStore{suspensions: []*subagentbatch.TurnSuspension{parkedChildRecord()}}
	source := NewBatchObligationSourceWithResolvers(store,
		stubAgentSessionObligationResolver{"child-live": false, "child-done": true},
		stubTeamObligationResolver{"team-live": false, "team-done": true},
	)

	rows, err := source.ListObligations(ctx, "root-session-1")
	require.NoError(t, err)
	require.Len(t, rows, 5, "record order: agent sessions first, then team runs")
	require.Equal(t, []string{
		"agent_session:child-live", "agent_session:child-done", "agent_session:child-missing",
		"team:team-live", "team:team-done",
	}, []string{rows[0].ID, rows[1].ID, rows[2].ID, rows[3].ID, rows[4].ID})

	require.Equal(t, "agent_session", rows[0].Kind)
	require.Equal(t, "turn-1", rows[0].ParentTurnID, "I3: the parked record's turn anchors the resume")
	require.Equal(t, ObligationStateRunning, rows[0].State)
	require.False(t, rows[0].Terminal)
	require.Equal(t, ObligationStateCompleted, rows[1].State)
	require.True(t, rows[1].Terminal)
	require.Equal(t, ObligationStatePending, rows[2].State, "a missing child row is never evidence of completion")
	require.False(t, rows[2].Terminal)
	require.Equal(t, "team", rows[3].Kind)
	require.Equal(t, ObligationStateRunning, rows[3].State)
	require.Equal(t, ObligationStateCompleted, rows[4].State)
	require.True(t, rows[4].Terminal)

	rc := BuildResumeContext(ctx, source, nil, ResumeContextRequest{ParentSessionID: "root-session-1"})
	require.Equal(t, 5, rc.TotalCount)
	require.Equal(t, 3, rc.PendingCount, "live child + live team + unreadable child all gate the join")
	require.False(t, rc.Terminal)
	require.Equal(t, "turn-1", rc.TurnID)
	require.Contains(t, rc.Text, "obligations: total=5 pending=3 can_finalize=false")
	require.Contains(t, rc.Text, "agent_session agent_session:child-live: status=running (not terminal)")
	prompt := AutoWakePromptFor(rc)
	require.Contains(t, prompt, "仍有未终态 obligation：不得收尾（I1）")
	require.NotContains(t, prompt, "请直接产出终局报告")

	// Once every child and the team reach terminal states the same projection
	// authorizes the final report (§16.4).
	settled := NewBatchObligationSourceWithResolvers(store,
		stubAgentSessionObligationResolver{"child-live": true, "child-done": true, "child-missing": true},
		stubTeamObligationResolver{"team-live": true, "team-done": true},
	)
	rc = BuildResumeContext(ctx, settled, nil, ResumeContextRequest{ParentSessionID: "root-session-1"})
	require.Equal(t, 0, rc.PendingCount)
	require.True(t, rc.Terminal)
	require.Contains(t, AutoWakePromptFor(rc), "所有 obligation 均已终态")
}

// TestBatchObligationSource_UnwiredResolversNeverFinalizeParkedChild pins the
// conservative direction: without the child-session control plane the row stays
// non-terminal. A host that cannot judge a child must never be told to finalize
// over it (the same rule TurnObligationsAllTerminal applies).
func TestBatchObligationSource_UnwiredResolversNeverFinalizeParkedChild(t *testing.T) {
	store := &fakeBatchStore{suspensions: []*subagentbatch.TurnSuspension{{
		TurnID: "turn-1", SessionID: "root-session-1",
		ObligationIDs: []string{subagentbatch.AgentSessionObligationID("child-live")},
	}}}

	rc := BuildResumeContext(context.Background(), NewBatchObligationSource(store), nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
	})
	require.Equal(t, 1, rc.PendingCount, "an unwired resolver must never read as completed")
	require.False(t, rc.Terminal)
	require.Contains(t, rc.Text, "agent_session agent_session:child-live: status=pending (not terminal)")
	require.Contains(t, AutoWakePromptFor(rc), "不得收尾")
}

// TestBatchObligationSource_SuspensionReadErrorNeverFinalizes pins the degraded
// path: an unreadable parked-record page must never produce can_finalize=true.
// The resume degrades to "unknown" (no finalize instruction) instead.
func TestBatchObligationSource_SuspensionReadErrorNeverFinalizes(t *testing.T) {
	store := &fakeBatchStore{suspensionErr: errors.New("parked records offline")}

	rc := BuildResumeContext(context.Background(), NewBatchObligationSource(store), nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
	})
	require.Equal(t, -1, rc.PendingCount, "an unreadable ledger is unknown, not empty")
	require.False(t, rc.Terminal)
	require.Contains(t, rc.Text, "obligations: unknown")
	require.Contains(t, rc.Text, "ledger_projection_error: parked records offline")
	require.NotContains(t, AutoWakePromptFor(rc), "请直接产出终局报告")
}

// TestResumePrompt_WaitFeedbackTriggerWording pins the trigger phrase: a
// wait-period fallback feedback wake is not a child lifecycle event, and saying
// so made the parent misread the wake as "a child finished" (2026-10-09).
func TestResumePrompt_WaitFeedbackTriggerWording(t *testing.T) {
	rc := BuildResumeContext(context.Background(), &staticObligationSource{obligations: []ObligationRef{
		{ID: "agent_session:child-live", Kind: "agent_session", ParentTurnID: "turn-1", State: ObligationStateRunning},
	}}, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		TurnID:          "turn-1",
		WakeReasons:     []string{WakeReasonWaitFeedback},
	})
	require.Equal(t, []string{WakeReasonWaitFeedback}, rc.WakeReasons)

	prompt := ResumePrompt(rc)
	require.Contains(t, prompt, "resume：等待期兜底反馈（无新进度）触发**同一 turn 续跑**")
	require.Contains(t, prompt, "不得收尾")
	require.NotContains(t, prompt, "请直接产出终局报告")

	// Every other wake family keeps the original trigger phrase byte-identical.
	rc.WakeReasons = nil
	require.Contains(t, ResumePrompt(rc), "resume：子任务生命周期事件触发**同一 turn 续跑**")
}

// TestWakeConsumer_BuildResumeCarriesWakeReasons pins the consumer wiring: the
// claimed wakes' reasons must reach the resume builder, otherwise the prompt
// falls back to the lifecycle trigger phrase.
func TestWakeConsumer_BuildResumeCarriesWakeReasons(t *testing.T) {
	var got ResumeContextRequest
	consumer := &WakeConsumer{ResumeBuilder: func(_ context.Context, req ResumeContextRequest) *ResumeContext {
		got = req
		return &ResumeContext{Text: "resume text"}
	}}
	rc := consumer.buildResume(context.Background(), "root-session-1", "root-session-1", []WakePending{
		{WakeID: "wake-1", WakeReason: WakeReasonWaitFeedback},
		{WakeID: "wake-2", WakeReason: WakeReasonObligationSettled},
	}, nil)
	require.NotNil(t, rc)
	require.Equal(t, []string{WakeReasonWaitFeedback, WakeReasonObligationSettled}, got.WakeReasons)
}
